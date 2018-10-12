package rap

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

// ExchangeID identifies a in-progress request/response.
type ExchangeID uint16

func (e ExchangeID) String() string {
	return fmt.Sprintf("[ID %04x]", uint16(e))
}

// ErrUnhandledRecordType is returned when a frame head record type is unknown or unexpected.
type ErrUnhandledRecordType struct{}

func (ErrUnhandledRecordType) Error() string { return "unhandled record type" }

// ErrMissingFrameHead is returned when an RAP frame was expected to have the HEAD bit set and contain a RAP record.
type ErrMissingFrameHead struct{}

func (ErrMissingFrameHead) Error() string { return "missing frame head" }

// ExchangeConnection is the interface that an Exchange needs in order to
// communicate with the outside world and clean up.
type ExchangeConnection interface {
	// ExchangeWrite allows an Exchange to write a FrameData
	ExchangeWrite(fd FrameData) error
	// ExchangeAbortChannel returns the channel that is closed when owner is closing
	ExchangeAbortChannel() <-chan struct{}
}

// exchangeDeadline is an abstraction for handling timeouts.
type exchangeDeadline struct {
	mu     sync.Mutex // Guards timer and cancel
	timer  *time.Timer
	cancel chan struct{} // Must be non-nil
}

func makeExchangeDeadline() exchangeDeadline {
	return exchangeDeadline{cancel: make(chan struct{})}
}

// set sets the point in time when the deadline will time out.
// A timeout event is signaled by closing the channel returned by waiter.
// Once a timeout has occurred, the deadline can be refreshed by specifying a
// t value in the future.
//
// A zero value for t prevents timeout.
func (d *exchangeDeadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil && !d.timer.Stop() {
		<-d.cancel // Wait for the timer callback to finish and close cancel
	}
	d.timer = nil

	// Time is zero, then there is no deadline.
	closed := isClosedChan(d.cancel)
	if t.IsZero() {
		if closed {
			d.cancel = make(chan struct{})
		}
		return
	}

	// Time in the future, setup a timer to cancel in the future.
	if dur := time.Until(t); dur > 0 {
		if closed {
			d.cancel = make(chan struct{})
		}
		d.timer = time.AfterFunc(dur, func() {
			close(d.cancel)
		})
		return
	}

	// Time in the past, so close immediately.
	if !closed {
		close(d.cancel)
	}
}

// wait returns a channel that is closed when the deadline is exceeded.
func (d *exchangeDeadline) wait() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel
}

func isClosedChan(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

type runState int32

const (
	runStateUnused  = runState(0) // idle
	runStateActive  = runState(1) // sent or received frame that needs ack
	runStateWaitFin = runState(2) // sent local final, needs remote final or final-ack
	runStateRecycle = runState(3) // recycling
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "deadline exceeded" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// Exchange is essentially a pipe. It maintains the state of a request-response
// or WebSocket connection, moves data between the RAP and HTTP connections and
// handles the flow control mechanism, which is a simple transmission
// window with intermittent ACKs from the receiver.
type Exchange struct {
	ID          ExchangeID         // Exchange ID
	conn        ExchangeConnection // the Conn that owns us
	onRecycle   func(*Exchange)    // function to call when Exchange is recycled
	ackCh       chan struct{}      // ack's from peer go in this
	readCh      chan FrameData     // data frames from peer go in this
	cmu         sync.Mutex         // guards localClosed and remoteClosed
	localClosed chan struct{}      // closed when Close() has been called

	// these are atomic to allow Exchange.String() to print the state without causing races
	localSentFinal  int32    // nonzero if local has sent it's final frame
	remoteSentFinal int32    // nonzero if remote has sent it's final frame
	sendWindow      int32    // number of frames still allowed to be in flight
	state           runState // nonzero if a header frame has been sent or received
	hijacked        int32    // nonzero if Hijack() was called

	wmu           sync.Mutex  // guards fdw
	fdw           FrameData   // FrameData being written to, nil after final frame sent
	rmu           sync.Mutex  // guards fdr
	fdr           FrameData   // FrameData being read from by fr
	fp            FrameParser // Frame parser (into fdr)
	readDeadline  exchangeDeadline
	writeDeadline exchangeDeadline
	serialNumber  uint32
}

var exchangeNextSerialNumber uint32

func (e *Exchange) isHijacked() bool {
	return atomic.LoadInt32(&e.hijacked) != 0
}

func (e *Exchange) hijacking() bool {
	return atomic.CompareAndSwapInt32(&e.hijacked, 0, 1)
}

func (e *Exchange) starting() bool {
	return atomic.CompareAndSwapInt32((*int32)(&e.state), int32(runStateUnused), int32(runStateActive))
}

func (e *Exchange) setRunState(state runState) {
	atomic.StoreInt32((*int32)(&e.state), int32(state))
}

func (e *Exchange) getRunState() runState {
	return runState(atomic.LoadInt32((*int32)(&e.state)))
}

func (e *Exchange) hasRemoteSentFinal() bool {
	return atomic.LoadInt32(&e.remoteSentFinal) != 0
}

func (e *Exchange) remoteSendingFinal() bool {
	if atomic.CompareAndSwapInt32(&e.remoteSentFinal, 0, 1) {
		close(e.readCh)
		return true
	}
	return false
}

func (e *Exchange) hasLocalSentFinal() bool {
	return atomic.LoadInt32(&e.localSentFinal) != 0
}

func (e *Exchange) localSendingFinal() bool {
	return atomic.CompareAndSwapInt32(&e.localSentFinal, 0, 1)
}

// Serial returns a string identifying the Exchange, containing the
// owning Conn's serial number, a colon, and this exchange's serial number.
// Note that the serial number is unrelated to the Exchange ID.
func (e *Exchange) Serial() string {
	connSerial := uint32(0)
	if c := e.getConn(); c != nil {
		connSerial = c.serialNumber
	}
	return fmt.Sprintf("%x:%x", connSerial, e.serialNumber)
}

func (e *Exchange) String() string {
	state := ""
	switch e.getRunState() {
	case runStateUnused:
		state = "IDLE  "
	case runStateActive:
		state = " RUN  "
	case runStateWaitFin:
		state = "  FIN "
	case runStateRecycle:
		state = "    RC"
	}
	hijacked := "  "
	if e.isHijacked() {
		hijacked = " HJ"
	}
	locF := "  "
	if e.hasLocalSentFinal() {
		locF = "LF"
	}
	remF := "  "
	if e.hasRemoteSentFinal() {
		remF = "RF"
	}

	return fmt.Sprintf("[Exchange %v %v %s %s %s %s (%d+%d)]",
		e.Serial(), e.ID, state, hijacked, locF, remF, e.getSendWindow(), len(e.ackCh))
}

// NewExchange creates a new exchange
func NewExchange(conn ExchangeConnection, exchangeID ExchangeID) (e *Exchange) {
	if exchangeID >= MuxerExchangeID {
		panic(fmt.Sprintf("illegal exchange ID %d", int(exchangeID)))
	}
	e = &Exchange{
		ID:            exchangeID,
		conn:          conn,
		sendWindow:    int32(SendWindowSize),
		ackCh:         make(chan struct{}, MaxSendWindowSize),
		readCh:        make(chan FrameData, MaxSendWindowSize),
		localClosed:   make(chan struct{}),
		readDeadline:  makeExchangeDeadline(),
		writeDeadline: makeExchangeDeadline(),
		serialNumber:  atomic.AddUint32(&exchangeNextSerialNumber, 1),
	}
	return
}

// SubmitFrame gives the Exchange an incoming FrameData.
// None of the frames seen may be conn control frames.
// A fd of nil indicates an EOF condition.
// If this function blocks, it will block all Exchanges on
// the Conn.
func (e *Exchange) SubmitFrame(fd FrameData) (err error) {
	// log.Print("SubmitFrame() ", e, fd)

	if fd.Header().HasFlow() {
		if fd.Header().IsAck() {
			FrameDataFree(fd)
			select {
			case e.ackCh <- struct{}{}:
			default:
				panic(fmt.Sprint("ACK would block"))
			}
		} else if fd.Header().IsFinal() {
			// a final frame
			e.receivedFinal(fd)
		}
		return
	}

	if e.hasRemoteSentFinal() {
		panic(fmt.Sprint(e, " received frame after final: ", fd))
	}

	select {
	case e.readCh <- fd:
	default:
		panic(fmt.Sprint("DATA would block: ", fd))
	}

	return
}

func (e *Exchange) receivedFinal(fd FrameData) {
	e.cmu.Lock()
	defer e.cmu.Unlock()
	// log.Print(" FIN ", e, fd)

	if fd != nil {
		if fd.Header().SizeValue() != 0 {
			panic("final frame has size value set")
		}
		FrameDataFree(fd)
	}

	if !e.remoteSendingFinal() {
		panic("received multiple final frames")
	}

	if e.hasLocalSentFinal() {
		e.recycle()
	}
}

// readFrame reads data frames from the read channel.
// None of the frames seen may be conn control frames.
// Also writes acknowledgement frames.
func (e *Exchange) readFrame() (err error) {
	// log.Print("Exchange.readFrame(): len(e.fdr)=", len(e.fdr), " e=", e)
	if e.fdr != nil {
		FrameDataFree(e.fdr)
		e.fdr = nil
		e.fp = nil
	}

	select {
	case e.fdr = <-e.readCh:
		if e.fdr != nil {
			e.fp = NewFrameParser(e.fdr)
			e.writeAckFrame()
		} else {
			err = errors.WithStack(io.EOF)
		}
	case <-e.readDeadline.wait():
		err = errors.WithStack(timeoutError{})
	case <-e.conn.ExchangeAbortChannel():
		err = errors.WithStack(serverClosedError{})
	case <-e.localClosed:
		err = errors.WithStack(io.ErrClosedPipe)
	}

	return
}

func (e *Exchange) writeAckFrame() {
	fd := FrameDataAllocID(e.ID)
	fd.Header().SetFlow()
	e.conn.ExchangeWrite(fd)
}

// LoadFrameReader ensures the frame reader has payload data or an error.
func (e *Exchange) LoadFrameReader() (err error) {
	e.rmu.Lock()
	defer e.rmu.Unlock()
	return e.loadFrameReader()
}

func (e *Exchange) loadFrameReader() (err error) {
	for len(e.fp) == 0 && err == nil {
		err = e.readFrame()
		// log.Print("Exchange.loadFrameReader(): ", e.ID, " readFrame() len(fr)=", len(e.fp), " err=", err, "  ", e)
	}
	return
}

// WriteStart prepares a new frame for writing.
func (e *Exchange) WriteStart() error {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	return e.writeStart()
}

func (e *Exchange) writeStart() error {
	if e.hasRemoteSentFinal() {
		return errors.WithStack(io.EOF)
	}
	select {
	case <-e.localClosed:
		return errors.WithStack(io.ErrClosedPipe)
	case <-e.writeDeadline.wait():
		return errors.WithStack(timeoutError{})
	default:
		if e.fdw == nil {
			// log.Print("Exchange.writeStart() (new fd)", e)
			e.fdw = FrameDataAllocID(e.ID)
		}
		return nil
	}
}

// Available returns number of free bytes in the current frame.
func (e *Exchange) Available() int {
	return e.fdw.Available()
}

// Buffered returns the number of bytes that have been written to the
// current frame, including the header size.
func (e *Exchange) Buffered() int {
	return e.fdw.Buffered()
}

// Implements io.Reader for Exchange.
// Used when copying data from a RAP connection to a HTTP body.
func (e *Exchange) Read(p []byte) (n int, err error) {
	e.rmu.Lock()
	defer e.rmu.Unlock()
	return e.read(p)
}

func (e *Exchange) read(p []byte) (n int, err error) {
	if err = e.loadFrameReader(); err == nil {
		n, err = e.fp.Read(p)
	}
	return
}

// ReadFrom implements io.ReaderFrom for Exchange body data.
// Used when copying data from a HTTP body to a RAP connection.
func (e *Exchange) ReadFrom(r io.Reader) (n int64, err error) {
	if r == nil {
		return 0, errors.WithStack(io.EOF)
	}
	for err == nil {
		var m int64
		m, err = e.readFromHelper(r)
		n += m
	}
	// io.ReaderFrom: Any error except io.EOF encountered during the read is also returned.
	if errors.Cause(err) == io.EOF {
		err = nil
	}
	return
}

func (e *Exchange) readFrom(r io.Reader) (n int64, err error) {
	for err == nil {
		var m int64
		m, err = e.readFromHelperLocked(r)
		n += m
	}
	// io.ReaderFrom: Any error except io.EOF encountered during the read is also returned.
	if errors.Cause(err) == io.EOF {
		err = nil
	}
	return
}

func (e *Exchange) readFromHelper(r io.Reader) (n int64, err error) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	return e.readFromHelperLocked(r)
}

func (e *Exchange) readFromHelperLocked(r io.Reader) (n int64, err error) {
	var count int
	if err = e.writeStart(); err == nil {
		maxCount := e.fdw.Available()
		if count, err = r.Read(e.fdw[len(e.fdw) : len(e.fdw)+maxCount]); count > 0 {
			if err != nil {
				err = errors.WithStack(err)
			}
			e.fdw.Header().SetBody()
			n += int64(count)
			e.fdw = e.fdw[:len(e.fdw)+count]
			if flushErr := e.flush(); flushErr != nil {
				if err == nil {
					err = flushErr
				}
			}
		}
	}
	return
}

// TODO: func (b *Writer) Reset(w io.Writer)

// Write implements io.Writer for Exchange, and is used to write body data.
func (e *Exchange) Write(p []byte) (n int, err error) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	// log.Print("Exchange.Write() len(p)=", len(p), " avail=", e.Available())
	if n, err = e.write(p); err == nil {
		if err = e.flush(); err != nil {
			n = 0
		}
	}
	return
}

func (e *Exchange) write(p []byte) (n int, err error) {
	err = e.writeStart()

	for err == nil && len(p) > e.fdw.Available() {
		var m int
		if m = e.fdw.Available(); m > 0 {
			e.fdw.Header().SetBody()
			e.fdw = append(e.fdw, p[:m]...)
			p = p[m:]
		}
		if err = e.flush(); err == nil {
			n += m
			err = e.writeStart()
		}
	}

	if err == nil {
		e.fdw.Header().SetBody()
		e.fdw = append(e.fdw, p...)
		n += len(p)
	}

	return
}

// WriteTo writes the body payload of the exchange to a io.Writer.
func (e *Exchange) WriteTo(w io.Writer) (n int64, err error) {
	for err == nil {
		var m int64
		m, err = e.writeToHelper(w)
		n += m
	}
	if errors.Cause(err) == io.EOF {
		err = nil
	}
	return
}

func (e *Exchange) writeToHelper(w io.Writer) (n int64, err error) {
	e.rmu.Lock()
	defer e.rmu.Unlock()
	if err = e.loadFrameReader(); err != nil {
		return
	}
	for len(e.fp) > 0 {
		var count int
		count, err = w.Write(e.fp)
		e.fp = e.fp[count:]
		n += int64(count)
		if err != nil && err != io.ErrShortWrite {
			return
		}
	}
	return
}

// WriteByte implements io.ByteWriter for Exchange.
func (e *Exchange) WriteByte(c byte) (err error) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	if err = e.writeByte(c); err == nil {
		err = e.flush()
	}
	return
}

func (e *Exchange) writeByte(c byte) (err error) {
	err = e.writeStart()

	if err == nil && e.fdw.Available() <= 0 {
		if err = e.flush(); err == nil {
			err = e.writeStart()
		}
	}

	if err == nil {
		e.fdw.Header().SetBody()
		err = e.fdw.WriteByte(c)
	}

	return
}

// TODO: func (b *Writer) WriteRune(r rune) (size int, err error)
// TODO: func (b *Writer) WriteString(s string) (int, error)

// Flush handles write flow control and injects the current frame into the conn.
// Note that the current write frame is expected to be a regular data frame,
// such that e.fdw.Header() returns false for IsConnControl() and true for
// HasPayload().
func (e *Exchange) Flush() (err error) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	return e.flush()
}

func (e *Exchange) flush() (err error) {
	if fd := e.fdw; fd != nil {
		e.fdw = nil
		err = e.writeFrame(fd)
	}
	return
}

func (e *Exchange) writeFrame(fd FrameData) (err error) {
	if fd.Header().HasFlow() {
		panic(fmt.Sprint("attempt to send flow control frame: ", e, fd))
	}

	if !fd.Header().HasBodyOrHead() {
		if len(fd) > FrameHeaderSize {
			panic(fmt.Sprint("missing head or body flag: ", e, fd))
		}
		// empty blank frame
		FrameDataFree(fd)
		return
	}

	if len(fd) > FrameMaxSize {
		return ErrFrameTooBig{}
	}

	fd.Header().SetSizeValue(len(fd) - FrameHeaderSize)

	// Consume ACKs and check for close conditions
	for err == nil {
		select {
		case <-e.ackCh:
			e.consumeAck()
		case <-e.localClosed:
			err = errors.WithStack(io.ErrClosedPipe)
		case <-e.conn.ExchangeAbortChannel():
			err = errors.WithStack(serverClosedError{})
		case <-e.writeDeadline.wait():
			err = errors.WithStack(timeoutError{})
		default:
			if e.hasRemoteSentFinal() {
				err = errors.WithStack(io.EOF)
			} else if e.getSendWindow() > 0 {
				// if the send window allows, go ahead and send it
				if atomic.AddInt32(&e.sendWindow, -1) < 0 {
					panic(fmt.Sprintf("sendWindow went negative: %+v\n", e))
				}
				e.starting()
				return e.conn.ExchangeWrite(fd)
			} else {
				// SendWindow is empty, so do a blocking wait for an ACK
				select {
				case <-e.ackCh:
					e.consumeAck()
				case <-e.localClosed:
					err = errors.WithStack(io.ErrClosedPipe)
				case <-e.conn.ExchangeAbortChannel():
					err = errors.WithStack(serverClosedError{})
				case <-e.writeDeadline.wait():
					err = errors.WithStack(timeoutError{})
				}
			}
		}
	}

	FrameDataFree(fd)
	return
}

func (e *Exchange) getSendWindow() int {
	return int(atomic.LoadInt32(&e.sendWindow))
}

func (e *Exchange) getConn() *Muxer {
	if c, ok := e.conn.(*Muxer); ok {
		return c
	}
	return nil
}

// recycle restores an Exchange to it's initial state and calls the onRecycle handler.
// Before calling, the local end must be closed. If the exchange is started,
// then remote must be closed as well. If it is not started, the remote must not be closed.
func (e *Exchange) recycle() {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	e.rmu.Lock()
	defer e.rmu.Unlock()
	e.recycleLocked()
}

func (e *Exchange) recycleLocked() {
	// log.Print("  RC ", e)

	e.setRunState(runStateRecycle)

	if !isClosedChan(e.localClosed) {
		panic(fmt.Sprintf("recycle(): local not closed\n%v\n", e))
	}
	if len(e.fdw) > 0 {
		panic(fmt.Sprintf("recycle(): still data left in fdw\n%v\n%v\n", e, e.fdw))
	}
	if !e.hasLocalSentFinal() {
		panic(fmt.Sprintf("recycle(): local has not sent final\n%v\n", e))
	}
	if !e.hasRemoteSentFinal() {
		panic(fmt.Sprintf("recycle(): remote has not sent final\n%v\n", e))
	}

	// drain ack and read channels
	for len(e.ackCh) > 0 {
		<-e.ackCh
	}
	for len(e.readCh) > 0 {
		FrameDataFree(<-e.readCh)
	}

	e.readCh = make(chan FrameData, MaxSendWindowSize)
	e.localClosed = make(chan struct{})
	atomic.StoreInt32(&e.localSentFinal, 0)
	atomic.StoreInt32(&e.remoteSentFinal, 0)
	atomic.StoreInt32(&e.sendWindow, int32(SendWindowSize))
	atomic.StoreInt32(&e.hijacked, 0)
	e.fdw = nil
	e.fdr = nil
	e.fp = nil
	e.writeDeadline.set(time.Time{})
	e.readDeadline.set(time.Time{})

	e.setRunState(runStateUnused)

	if e.onRecycle != nil {
		e.onRecycle(e)
	}
}

func (e *Exchange) makeFinalFrame(isAck bool) (fd FrameData) {
	if fd = FrameDataAllocID(e.ID); fd != nil {
		fd.Header().SetFlow()
		fd.Header().SetBody()
		if isAck {
			fd.Header().SetHead()
		}
	}
	return
}

func (e *Exchange) writeFinalLocked(isAck bool) {
	if e.localSendingFinal() {
		e.flush()
		if fd := e.makeFinalFrame(isAck); fd != nil {
			e.conn.ExchangeWrite(fd)
		}
	}
}

func (e *Exchange) consumeAck() {
	if atomic.AddInt32(&e.sendWindow, 1) > int32(SendWindowSize) {
		panic(fmt.Sprintf("sendWindow %d > %d SendWindowSize: %+v\n", e.getSendWindow(), int32(SendWindowSize), e))
	}
}

// Close unblocks all blocked calls to Read and Write, sends the final frame and
// discards any pending data in the receive queue.
// If the remote is closed, it recycles the Exchange.
func (e *Exchange) Close() error {
	e.cmu.Lock()
	defer e.cmu.Unlock()

	// log.Print("  CL ", e)

	select {
	case <-e.localClosed:
		// already closed
		return errors.WithStack(io.ErrClosedPipe)
	default:
		close(e.localClosed)
	}

	e.wmu.Lock()
	defer e.wmu.Unlock()
	e.rmu.Lock()
	defer e.rmu.Unlock()

	if !e.hasRemoteSentFinal() {
		// will be recycled when remote is closed
		e.writeFinalLocked(false)
		e.setRunState(runStateWaitFin)
		return nil
	}

	e.writeFinalLocked(true)
	e.recycleLocked()

	return nil
}

func (e *Exchange) closeIfNotHijacked() error {
	if e.isHijacked() {
		return nil
	}
	return e.Close()
}

// OnRecycle sets the callback to be invoked when the exchange is being recycled.
// Set to nil to disable the callback. You may *not* call this function from the
// callback itself, as that will deadlock.
func (e *Exchange) OnRecycle(onRecycle func(*Exchange)) {
	e.cmu.Lock()
	defer e.cmu.Unlock()
	e.onRecycle = onRecycle
}

// WriteUserRecordType writes a user record marker and sets the head bit.
func (e *Exchange) WriteUserRecordType(c byte) (err error) {
	if c < 0x80 {
		return ErrUnhandledRecordType{}
	}
	if err = e.WriteStart(); err == nil {
		e.fdw.WriteRecordType(RecordType(c))
	}
	return
}

// WriteRequest writes a http.Request to the exchange, including it's Body.
func (e *Exchange) WriteRequest(r *http.Request) (err error) {
	if err = e.WriteStart(); err == nil {
		if err = e.fdw.WriteRequest(r); err == nil {
			if r.ContentLength > 0 {
				if _, err = io.CopyN(e, r.Body, r.ContentLength); err != nil {
					err = errors.WithStack(err)
				}
			} else {
				_, err = e.ReadFrom(r.Body)
			}
			if flushErr := e.Flush(); err == nil {
				err = flushErr
			}
		}
	}
	return
}

// ProxyResponse reads a HTTP response but not it's body from the Exchange data
// and writes it to the given http.ResponseWriter.
func (e *Exchange) ProxyResponse(w http.ResponseWriter) (statusCode int, err error) {
	e.rmu.Lock()
	defer e.rmu.Unlock()

	if err = e.loadFrameReader(); err != nil {
		return 0, err
	}

	if e.fdr.Header().HasHead() {
		switch e.fp.ReadRecordType() {
		case RecordTypeHTTPResponse:
			statusCode = e.fp.ProxyResponse(w)
		case RecordTypeHijacked:
			statusCode = 101
		default:
			return 0, errors.WithStack(ErrUnhandledRecordType{})
		}
	}

	return
}

// WriteResponse writes a http.Response to the exchange.
func (e *Exchange) WriteResponse(r *http.Response) (err error) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	if err = e.writeResponseData(r.StatusCode, r.ContentLength, r.Header); err == nil {
		if err == nil && r.Body != nil {
			if r.ContentLength > 0 {
				_, err = io.CopyN(e, r.Body, r.ContentLength)
			} else {
				_, err = e.readFrom(r.Body)
			}
		}
		if flushErr := e.flush(); err == nil {
			err = flushErr
		}
	}
	return
}

// WriteResponseData writes a RAP response header.
func (e *Exchange) WriteResponseData(code int, contentLength int64, header http.Header) (err error) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	return e.writeResponseData(code, contentLength, header)
}

func (e *Exchange) writeResponseData(code int, contentLength int64, header http.Header) (err error) {
	if err = e.writeStart(); err == nil {
		err = e.fdw.WriteResponse(code, contentLength, header)
	}
	return
}

// Serve waits for a start frame and then invokes the given http.Handler.
func (e *Exchange) Serve(h http.Handler) (err error) {
	defer e.closeIfNotHijacked()
	if err = e.LoadFrameReader(); err == nil {
		if e.fdr.Header().HasHead() {
			switch rt := e.fp.ReadRecordType(); rt {
			case RecordTypeHTTPRequest:
				var req *http.Request
				req, err = e.fp.ReadRequest()
				if err == nil {
					// log.Printf("rap.Exchange.Serve(): %+v\n", req)
					req.Body = e
					h.ServeHTTP(&ResponseWriter{Exchange: e}, req)
					// if the handler left things in the buffer, flush it
					if flushErr := e.Flush(); err == nil {
						err = flushErr
					}
				}
			case RecordTypeHijacked:
				e.hijacking()
			default:
				err = errors.Wrapf(ErrUnhandledRecordType{}, "type code '%02x'", int(rt))
			}
		} else {
			err = errors.Wrapf(ErrMissingFrameHead{}, "%v", e.fdr)
		}
	}
	return
}

// Hijack lets the caller take over the connection.
// After a call to Hijack the HTTP server library
// will not do anything else with the connection.
func (e *Exchange) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	if e.hijacking() {
		e.starting()
		fd := NewFrameDataID(e.ID)
		fd.WriteRecordType(RecordTypeHijacked)
		return e, bufio.NewReadWriter(bufio.NewReader(e), bufio.NewWriter(e)), e.writeFrame(fd)
	}
	return nil, nil, errors.Errorf("already hijacked")
}

type exchangeAddr struct{}

func (exchangeAddr) Network() string { return "rap" }
func (exchangeAddr) String() string  { return "rap" }

// LocalAddr returns the local network address stub.
func (e *Exchange) LocalAddr() net.Addr {
	return exchangeAddr{}
}

// RemoteAddr returns remote network address stub.
func (e *Exchange) RemoteAddr() net.Addr {
	return exchangeAddr{}
}

// SetDeadline sets the read and write deadlines associated
// with the connection. It is equivalent to calling both
// SetReadDeadline and SetWriteDeadline.
func (e *Exchange) SetDeadline(t time.Time) error {
	if e.hasLocalSentFinal() {
		return errors.WithStack(io.ErrClosedPipe)
	}
	e.readDeadline.set(t)
	e.writeDeadline.set(t)
	return nil
}

// SetReadDeadline sets the deadline for future Read calls
// and any currently-blocked Read call.
// A zero value for t means Read will not time out.
func (e *Exchange) SetReadDeadline(t time.Time) error {
	if e.hasLocalSentFinal() {
		return errors.WithStack(io.ErrClosedPipe)
	}
	e.readDeadline.set(t)
	return nil
}

// SetWriteDeadline sets the deadline for future Write calls
// and any currently-blocked Write call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means Write will not time out.
func (e *Exchange) SetWriteDeadline(t time.Time) error {
	if e.hasLocalSentFinal() {
		return errors.WithStack(io.ErrClosedPipe)
	}
	e.writeDeadline.set(t)
	return nil
}
