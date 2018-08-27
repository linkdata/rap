package rap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ExchangeID identifies a in-progress request/response.
type ExchangeID uint16

func (e ExchangeID) String() string {
	return fmt.Sprintf("[ExchangeID %04x]", uint16(e))
}

var (
	// ErrTimeoutFlowControl is returned when a the flow control window doesn't reach parity in time.
	ErrTimeoutFlowControl = errors.New("flow control timeout")
	// ErrUnhandledRecordType is returned when a frame head record type is unknown or unexpected.
	ErrUnhandledRecordType = errors.New("unhandled record type")
	// ErrMissingFrameHead is returned when a frame was expected to have a head part but did not.
	ErrMissingFrameHead = errors.New("missing frame head")
)

// ExchangeConnection is the interface that an Exchange needs in order to
// communicate with the outside world and clean up.
type ExchangeConnection interface {
	// ExchangeWrite allows an Exchange to write a FrameData
	ExchangeWrite(fd FrameData) error
	// ExchangeRelease returns the Exchange to the Conn, allowing it to
	// be re-used for other requests.
	// ExchangeRelease(*Exchange)

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

type timeoutError struct{}

func (timeoutError) Error() string   { return "deadline exceeded" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// Exchange is essentially a pipe. It maintains the state of a request-response
// or WebSocket connection, moves data between the RAP and HTTP connections and
// handles the flow control mechanism, which is a simple transmission
// window with intermittent ACKs from the receiver.
type Exchange struct {
	ID            ExchangeID         // Exchange ID
	conn          ExchangeConnection // the Conn that owns us
	onRecycle     func(*Exchange)    // function to call when Exchange is recycled
	ackCh         chan struct{}      // ack's from peer go in this
	readCh        chan FrameData     // data frames from peer go in this
	cmu           sync.Mutex         // guards localClosed
	localClosed   chan struct{}      // closed when Close() has been called
	remoteClosed  chan struct{}      // closed when final frame has been received
	sendWindow    int32              // number of frames still allowed to be in flight
	wmu           sync.Mutex         // guards fdw
	fdw           FrameData          // FrameData being written to, nil after final frame sent
	rmu           sync.Mutex         // guards fdr
	fdr           FrameData          // FrameData being read from by fr
	fp            FrameParser        // Frame parser (into fdr)
	didStart      int32              // nonzero if the exchange has sent or received the first frame
	didSendFinal  int32              // nonzero if we have sent our final frame
	didRecycle    int32              // nonzero if recycling in progress
	didHijack     int32              // nonzero if Hijack() was called
	readDeadline  exchangeDeadline
	writeDeadline exchangeDeadline
}

func (e *Exchange) hasStarted() bool {
	return atomic.LoadInt32(&e.didStart) != 0
}

func (e *Exchange) started() {
	atomic.StoreInt32(&e.didStart, 1)
}

func (e *Exchange) isHijacked() bool {
	return atomic.LoadInt32(&e.didHijack) != 0
}

func (e *Exchange) hijacked() {
	atomic.StoreInt32(&e.didHijack, 1)
}

func (e *Exchange) hasReceivedFinal() bool {
	return isClosedChan(e.remoteClosed)
}

func (e *Exchange) hasSentFinal() bool {
	return atomic.LoadInt32(&e.didSendFinal) != 0
}

func (e *Exchange) sendingFinal() bool {
	return atomic.CompareAndSwapInt32(&e.didSendFinal, 0, 1)
}

func (e *Exchange) recycling() bool {
	return atomic.CompareAndSwapInt32(&e.didRecycle, 0, 1)
}

func (e *Exchange) hasLocalClosed() bool {
	return isClosedChan(e.localClosed)
}

func (e *Exchange) String() string {
	return fmt.Sprintf("[Exchange %v sendW=%v started=%v sentC=%v recvC=%v len(ackCh)=%d]",
		e.ID, e.getSendWindow(), e.hasStarted(), e.hasSentFinal(), e.hasReceivedFinal(), len(e.ackCh))
}

// NewExchange creates a new exchange
func NewExchange(conn ExchangeConnection, exchangeID ExchangeID) (e *Exchange) {
	e = &Exchange{
		ID:            exchangeID,
		conn:          conn,
		sendWindow:    int32(SendWindowSize),
		ackCh:         make(chan struct{}, MaxSendWindowSize),
		readCh:        make(chan FrameData, MaxSendWindowSize),
		localClosed:   make(chan struct{}),
		remoteClosed:  make(chan struct{}),
		readDeadline:  makeExchangeDeadline(),
		writeDeadline: makeExchangeDeadline(),
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
	if fd == nil || fd.Header().IsFinal() {
		// final frame
		e.recievedFinal(fd)
	} else if fd.IsAck() {
		// ack frame
		FrameDataFree(fd)
		select {
		case e.ackCh <- struct{}{}:
		default:
			panic(fmt.Sprint("ACK would block: ", fd))
		}
	} else {
		// data frame
		select {
		case e.readCh <- fd:
		default:
			panic(fmt.Sprint("DATA would block: ", fd))
		}
	}

	return
}

func (e *Exchange) recievedFinal(fd FrameData) {
	e.cmu.Lock()
	defer e.cmu.Unlock()
	if fd != nil {
		if fd.Header().HasPayload() {
			panic(fmt.Sprint("final frame has payload: ", fd))
		}
		FrameDataFree(fd)
	}
	select {
	case <-e.remoteClosed:
		if fd != nil {
			panic("received multiple final frames")
		}
	default:
		close(e.remoteClosed)
	}
	select {
	case <-e.localClosed:
		e.recycle()
	default:
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
	case <-e.readDeadline.wait():
		err = timeoutError{}
	case <-e.conn.ExchangeAbortChannel():
		err = ErrServerClosed
	case <-e.localClosed:
		err = io.EOF
	case <-e.remoteClosed:
		// make sure to return anything still in the input queue
		select {
		case e.fdr = <-e.readCh:
		default:
			err = io.EOF
		}
	}

	if err == nil {
		e.fp = NewFrameParser(e.fdr)
		e.writeAckFrame()
	} else {
		// e.handleReadError()
	}

	return
}

/*
func (e *Exchange) handleReadError() {
	// make sure we send ACK's as needed and consume them
	for {
		select {
		case fd := <-e.readCh:
			FrameDataFree(fd)
			e.writeAckFrame()
		case <-e.ackCh:
			e.consumeAck()
		default:
			return
		}
	}
}
*/

func (e *Exchange) writeAckFrame() {
	e.writeAckFrameUsing(FrameDataAlloc())
}

func (e *Exchange) writeAckFrameUsing(fd FrameData) {
	fd.ClearID(e.ID)
	e.conn.ExchangeWrite(fd)
}

// LoadFrameReader ensures the frame reader has payload data or an error.
func (e *Exchange) LoadFrameReader() (err error) {
	e.rmu.Lock()
	defer e.rmu.Unlock()
	return e.loadFrameReader()
}

func (e *Exchange) loadFrameReader() (err error) {
	e.started()
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
	switch {
	case e.hasSentFinal():
		return io.ErrClosedPipe
	case e.hasReceivedFinal():
		return io.ErrClosedPipe
	case e.hasLocalClosed():
		return io.ErrClosedPipe
	case isClosedChan(e.writeDeadline.wait()):
		return timeoutError{}
	}
	e.started()
	if e.fdw == nil {
		// log.Print("Exchange.writeStart() (new fd)", e)
		e.fdw = FrameDataAllocID(e.ID)
	}
	return nil
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
		return 0, io.EOF
	}
	for err == nil {
		var m int64
		m, err = e.readFromHelper(r)
		n += m
	}
	// io.ReaderFrom: Any error except io.EOF encountered during the read is also returned.
	if err == io.EOF {
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
	if err == io.EOF {
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
			e.fdw.Header().SetBody()
			n += int64(count)
			e.fdw = e.fdw[:len(e.fdw)+count]
		}
		if flushErr := e.flush(); flushErr != nil {
			if err == nil {
				err = flushErr
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
			// log.Print("Exchange.Write() len(p)=", len(p), " avail=", e.Available(), " m=", m)
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
	if err == io.EOF {
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
	return e.writeByte(c)
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
	switch {
	case e.hasSentFinal():
		return io.ErrClosedPipe
	case e.hasLocalClosed():
		return io.ErrClosedPipe
	case e.hasReceivedFinal():
		return io.ErrClosedPipe
	case isClosedChan(e.writeDeadline.wait()):
		return timeoutError{}
	}

	if fd.Header().IsFinal() {
		panic(fmt.Sprint("attempt to send final frame from other than Close(): ", fd))
	}

	if len(fd) > FrameMaxSize {
		FrameDataFree(fd)
		return ErrFrameTooBig
	}

	e.started()

	if fd.Header().HasPayload() {
		// make sure window allows us to send
		fd.Header().SetSizeValue(len(fd) - FrameHeaderSize)
		if err = e.waitForSendWindowSize(1); err != nil {
			return err
		}
		if atomic.AddInt32(&e.sendWindow, -1) < 0 {
			panic(fmt.Sprintf("sendWindow went negative: %+v\n", e))
		}
	}

	err = e.conn.ExchangeWrite(fd)

	return
}

func (e *Exchange) getSendWindow() int {
	return int(atomic.LoadInt32(&e.sendWindow))
}

func (e *Exchange) waitForSendWindowSize(minimumRequiredWindowSize int) error {
	// timer := time.NewTimer(e.conn.ExchangeTimeout())
	// defer timer.Stop()

	for e.getSendWindow() < minimumRequiredWindowSize {
		select {
		case <-e.ackCh:
			e.consumeAck()
		case <-e.localClosed:
			return io.ErrClosedPipe
		case <-e.remoteClosed:
			return io.ErrClosedPipe
		case <-e.conn.ExchangeAbortChannel():
			return ErrServerClosed
		case <-e.writeDeadline.wait():
			return timeoutError{}
		}
	}

	return nil
}

func (e *Exchange) getConn() *Conn {
	if c, ok := e.conn.(*Conn); ok {
		return c
	}
	return nil
}

// Recycle restores an Exchange to it's initial state and calls the onRecycle handler.
// Either both localClosed and remoteClosed channels must be closed, or the Exchange
// must be unused.
func (e *Exchange) recycle() {
	if e.recycling() {
		e.wmu.Lock()
		defer e.wmu.Unlock()
		e.rmu.Lock()
		defer e.rmu.Unlock()

		// log.Print("  RC ", e.getConn(), e)

		if isClosedChan(e.localClosed) && isClosedChan(e.remoteClosed) {
		drain:
			for {
				select {
				case <-e.ackCh:
				case fd := <-e.readCh:
					FrameDataFree(fd)
				default:
					break drain
				}
			}
			e.localClosed = make(chan struct{})
			e.remoteClosed = make(chan struct{})
		} else {
			panic("recycle() requires both local and remote channels closed")
		}
		atomic.StoreInt32(&e.sendWindow, int32(SendWindowSize))
		atomic.StoreInt32(&e.didStart, 0)
		atomic.StoreInt32(&e.didSendFinal, 0)
		atomic.StoreInt32(&e.didHijack, 0)
		e.fdw = nil
		e.fdr = nil
		e.fp = nil

		if e.onRecycle != nil {
			e.onRecycle(e)
		}
		atomic.StoreInt32(&e.didRecycle, 0)
	}
}

func (e *Exchange) writeFinal() bool {
	if e.sendingFinal() {
		if fd := FrameDataAllocID(e.ID); fd != nil {
			fd.Header().SetFinal()
			e.conn.ExchangeWrite(fd)
			return true
		}
	}
	return false
}

func (e *Exchange) consumeAck() {
	if atomic.AddInt32(&e.sendWindow, 1) > int32(SendWindowSize) {
		panic(fmt.Sprintf("sendWindow %d > %d SendWindowSize: %+v\n", e.getSendWindow(), int32(SendWindowSize), e))
	}
}

// Close unblocks all blocked calls to Read and Write, sends the final frame and
// discards any pending data in the receive queue.
// If the remote is closed, it recycles the Exchange.
func (e *Exchange) Close() (err error) {
	e.cmu.Lock()
	defer e.cmu.Unlock()

	// log.Print("  CL ", e.getConn(), e)
	// debug.PrintStack()

	select {
	case <-e.localClosed:
		// already closed
		return io.ErrClosedPipe
	default:
		close(e.localClosed)
		if e.hasStarted() {
			e.writeFinal()
		}
	}

	select {
	case <-e.remoteClosed:
		e.recycle()
	default:
		if !e.hasStarted() {
			// unused, recycle immediately
			close(e.remoteClosed)
			e.recycle()
		}
	}

	return nil
}

// OnRecycle sets the callback to be invoked when the exchange is being recycled.
// Set to nil to disable the callback. You may *not* call this function from the
// callback itself, as that will deadlock.
func (e *Exchange) OnRecycle(onRecycle func(*Exchange)) {
	e.cmu.Lock()
	defer e.cmu.Unlock()
	e.onRecycle = onRecycle
}

// WriteRequest writes a http.Request to the exchange, including it's Body.
func (e *Exchange) WriteRequest(r *http.Request) (err error) {
	if err = e.WriteStart(); err == nil {
		if err = e.fdw.WriteRequest(r); err == nil {
			if r.ContentLength > 0 {
				_, err = io.CopyN(e, r.Body, r.ContentLength)
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
			return 0, ErrUnhandledRecordType
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

// RepeatServeHTTP repeatedly calls ServeHTTP() until an error occurs
func (e *Exchange) RepeatServeHTTP(h http.Handler) (err error) {
	recycleCh := make(chan struct{})
	e.OnRecycle(func(e *Exchange) {
		select {
		case recycleCh <- struct{}{}:
		default:
		}
	})
	defer e.OnRecycle(nil)
	for err == nil {
		err = e.ServeHTTP(h)
		if err == nil {
			select {
			case <-recycleCh:
			case <-e.conn.ExchangeAbortChannel():
				err = ErrServerClosed
			}
		}
	}
	return
}

func (e *Exchange) closeIfNotHijacked() {
	if !e.isHijacked() {
		e.Close()
	}
}

// ServeHTTP waits for a start frame and then invokes the given http.Handler.
func (e *Exchange) ServeHTTP(h http.Handler) (err error) {
	defer e.closeIfNotHijacked()
	if err = e.LoadFrameReader(); err != nil {
		return
	}
	if !e.fdr.Header().HasHead() {
		return ErrMissingFrameHead
	}
	switch rt := e.fp.ReadRecordType(); rt {
	case RecordTypeHTTPRequest:
		req, err := e.fp.ReadRequest()
		if err != nil {
			return err
		}
		// log.Printf("rap.Exchange.ServeHTTP(): %+v\n", req)
		req.Body = e
		h.ServeHTTP(&ResponseWriter{Exchange: e}, req)
		// if the handler left things in the buffer, flush it
		if flushErr := e.Flush(); err == nil {
			err = flushErr
		}
	case RecordTypeHijacked:
		e.hijacked()
	default:
		panic(fmt.Sprint("unhandled record type ", rt))
		// return ErrUnhandledRecordType
	}
	return
}

// Hijack lets the caller take over the connection.
// After a call to Hijack the HTTP server library
// will not do anything else with the connection.
func (e *Exchange) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	e.hijacked()
	fd := NewFrameDataID(e.ID)
	fd.WriteRecordType(RecordTypeHijacked)
	fd.Header().SetHead()
	return e, bufio.NewReadWriter(bufio.NewReader(e), bufio.NewWriter(e)), e.writeFrame(fd)
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
	if e.hasLocalClosed() {
		return io.ErrClosedPipe
	}
	e.readDeadline.set(t)
	e.writeDeadline.set(t)
	return nil
}

// SetReadDeadline sets the deadline for future Read calls
// and any currently-blocked Read call.
// A zero value for t means Read will not time out.
func (e *Exchange) SetReadDeadline(t time.Time) error {
	if e.hasLocalClosed() {
		return io.ErrClosedPipe
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
	if e.hasLocalClosed() {
		return io.ErrClosedPipe
	}
	e.writeDeadline.set(t)
	return nil
}
