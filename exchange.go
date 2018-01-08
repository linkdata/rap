package rap

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
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
	ExchangeRelease(*Exchange)
	// ExchangeTimeout returns the timeout duration to be used by the Exchange.
	ExchangeTimeout() time.Duration
}

// Exchange is essentially a pipe. It maintains the state of a request-response
// or WebSocket connection, moves data between the RAP and HTTP connections and
// handles the flow control mechanism, which is a simple transmission
// window with intermittent ACKs from the receiver.
type Exchange struct {
	ID               ExchangeID // Exchange ID
	conn             ExchangeConnection
	ackCh            chan struct{}
	readCh           chan FrameData
	doneChan         chan struct{}
	mu               sync.Mutex
	sendWindow       int         // number of frames still allowed to be in flight
	fdw              FrameData   // FrameData being written to, nil after final frame sent
	fdr              FrameData   // FrameData being read from by fr
	fp               FrameParser // Frame parser (into fdr)
	hasStarted       bool        // true if the exchange has sent or received the first frame
	hasReceived      bool        // true if the exchange has received the first frame
	hasSentFinal     bool        // true if we have sent our final frame
	hasReceivedFinal bool        // true if the peer has sent a it's final frame
}

func (e *Exchange) String() string {
	return fmt.Sprintf("[Exchange %v sendW=%v started=%v sentC=%v recvC=%v len(ackCh)=%d]",
		e.ID, e.sendWindow, e.hasStarted, e.hasSentFinal, e.hasReceivedFinal, len(e.ackCh))
}

// NewExchange creates a new exchange
func NewExchange(conn ExchangeConnection, exchangeID ExchangeID) (e *Exchange) {
	e = &Exchange{
		ID:         exchangeID,
		conn:       conn,
		sendWindow: SendWindowSize,
		ackCh:      make(chan struct{}, MaxSendWindowSize),
		readCh:     make(chan FrameData, MaxSendWindowSize),
		doneChan:   make(chan struct{}),
	}
	return
}

func (e *Exchange) closeDoneChanLocked() {
	select {
	case <-e.doneChan:
	default:
		close(e.doneChan)
	}
}

// SubmitFrame gives the Exchange an incoming FrameData.
// None of the frames seen may be conn control frames.
// A fd of nil indicates an EOF condition.
func (e *Exchange) SubmitFrame(fd FrameData) error {
	if fd != nil && !fd.Header().HasPayload() {
		FrameDataFree(fd)
		select {
		case e.ackCh <- struct{}{}:
			return nil
		case <-e.doneChan:
			return io.ErrClosedPipe
		}
	}

	select {
	case e.readCh <- fd:
		return nil
	case <-e.doneChan:
		return io.ErrClosedPipe
	}
}

// readFrame reads data frames from the read channel.
// None of the frames seen may be conn control frames.
// Also writes acknowledgement frames.
func (e *Exchange) readFrame() (err error) {
	// log.Print("Exchange.readFrame(): len(fr)=", len(e.fr), " e=", e)
	if e.fdr != nil {
		FrameDataFree(e.fdr)
		e.fdr = nil
		e.fp = nil
	}

	if e.hasReceivedFinal {
		// log.Print("Exchange.readFrame(): EOF e=", e)
		return io.EOF
	}

	select {
	case e.fdr = <-e.readCh:
		if e.fdr == nil {
			return io.EOF
		}
	case <-e.doneChan:
		return io.EOF
	}

	e.fp = NewFrameParser(e.fdr)
	if e.fdr.Header().IsFinal() {
		e.hasReceivedFinal = true
		return
	}
	// Write ack frame
	e.conn.ExchangeWrite(FrameDataAllocID(e.ID))
	return
}

// loadFrameReader ensures the frame reader has payload data or an error.
func (e *Exchange) loadFrameReader() (err error) {
	// log.Print("Exchange.loadFrameReader(): ", e.ID, " len(fr)=", len(e.fr))
	for len(e.fp) == 0 && err == nil {
		err = e.readFrame()
		// log.Print("Exchange.loadFrameReader(): ", e.ID, " readFrame() len(fr)=", len(e.fr), " err=", err, "  ", e)
	}
	return
}

// writeStart prepares a new frame for writing.
func (e *Exchange) writeStart() {
	if e.fdw == nil {
		// log.Print("Exchange.writeStart() (new fd)", e)
		e.fdw = FrameDataAllocID(e.ID)
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
	// log.Print("Exchange.Read([", len(p), "]) ", e)
	if err = e.loadFrameReader(); err == nil {
		n, err = e.fp.Read(p)
	}
	return
}

// ReadFrom implements io.ReaderFrom for Exchange body data.
// Used when copying data from a HTTP body to a RAP connection.
func (e *Exchange) ReadFrom(r io.Reader) (n int64, err error) {
	// log.Print("Exchange.ReadFrom() ", e)
	if r == nil {
		return 0, io.EOF
	}
	e.writeStart()
	for {
		var count int
		maxCount := e.fdw.Available()
		count, err = r.Read(e.fdw[len(e.fdw) : len(e.fdw)+maxCount])
		if count > 0 {
			e.fdw.Header().SetBody()
			n += int64(count)
			e.fdw = e.fdw[:len(e.fdw)+count]
		}
		if err != nil {
			break
		}
		if count == maxCount {
			if flushErr := e.Flush(); flushErr != nil {
				if err == nil {
					err = flushErr
				}
				break
			}
			e.writeStart()
		}
	}
	// io.ReaderFrom: Any error except io.EOF encountered during the read is also returned.
	if err == io.EOF {
		err = nil
	}
	return
}

// TODO: func (b *Writer) Reset(w io.Writer)

// Write implements io.Writer for Exchange, and is used to write body data.
func (e *Exchange) Write(p []byte) (n int, err error) {
	// log.Print("Exchange.Write([", len(p), "]) ", e)

	e.writeStart()
	for len(p) > e.fdw.Available() {
		if m := e.fdw.Available(); m > 0 {
			// log.Print("Exchange.Write() len(p)=", len(p), " avail=", e.Available(), " m=", m)
			e.fdw.Header().SetBody()
			e.fdw = append(e.fdw, p[:m]...)
			p = p[m:]
			n += m
		}
		if err = e.Flush(); err != nil {
			return
		}
		e.writeStart()
	}

	e.fdw.Header().SetBody()
	e.fdw = append(e.fdw, p...)
	n += len(p)
	return
}

// WriteTo writes the body payload of the exchange to a io.Writer.
func (e *Exchange) WriteTo(w io.Writer) (n int64, err error) {
	// log.Print("Exchange.WriteTo() ", e)
	for err == nil {
		if err = e.loadFrameReader(); err != nil {
			break
		}
		for len(e.fp) > 0 {
			var count int
			count, err = w.Write(e.fp)
			e.fp = e.fp[count:]
			n += int64(count)
			if err != nil && err != io.ErrShortWrite {
				break
			}
		}
	}
	if err == io.EOF {
		err = nil
	}
	return
}

// WriteByte implements io.ByteWriter for Exchange.
func (e *Exchange) WriteByte(c byte) error {
	e.writeStart()
	if e.fdw.Available() <= 0 {
		if err := e.Flush(); err != nil {
			return err
		}
		e.writeStart()
	}
	e.fdw.Header().SetBody()
	return e.fdw.WriteByte(c)
}

// TODO: func (b *Writer) WriteRune(r rune) (size int, err error)
// TODO: func (b *Writer) WriteString(s string) (int, error)

// Flush handles write flow control and injects the current frame into the conn.
// Note that the current write frame is expected to be a regular data frame,
// such that e.fdw.Header() returns false for IsConnControl() and true for
// HasPayload().
func (e *Exchange) Flush() (err error) {
	// log.Print("Exchange.Flush() ", e)
	if e.hasSentFinal {
		return io.ErrClosedPipe
	}

	if e.fdw == nil {
		return
	}

	if len(e.fdw) > FrameMaxSize {
		e.fdw = nil
		e.sendFinal()
		return ErrFrameTooBig
	}

	e.hasStarted = true

	if e.fdw.Header().IsFinal() {
		// final frame is not acked
		e.hasSentFinal = true
	} else {
		// make sure window allows us to send
		if err := e.waitForSendWindowSize(1); err != nil {
			return err
		}
		e.sendWindow--
	}

	if e.fdw.Header().HasPayload() {
		e.fdw.Header().SetSizeValue(len(e.fdw) - FrameHeaderSize)
	}

	err = e.conn.ExchangeWrite(e.fdw)
	e.fdw = nil

	return
}

func (e *Exchange) sendFinal() (err error) {
	if e.hasStarted && !e.hasSentFinal {
		e.hasSentFinal = true
		fdc := FrameDataRecycleID(e.fdw, e.ID)
		fdc.Header().SetFinal()
		err = e.conn.ExchangeWrite(fdc)
		e.fdw = nil
	}
	return
}

func (e *Exchange) waitForSendWindowSize(minimumRequiredWindowSize int) error {
	if e.sendWindow >= minimumRequiredWindowSize {
		return nil
	}

	timer := time.NewTimer(e.conn.ExchangeTimeout())
	defer timer.Stop()

	for e.sendWindow < minimumRequiredWindowSize {
		select {
		case <-e.doneChan:
			return io.ErrClosedPipe
		case _, ok := <-e.ackCh:
			if !ok {
				e.sendFinal()
				return io.ErrClosedPipe
			}
			e.sendWindow++
		case <-timer.C:
			// log.Print("Exchange.waitForSendWindowSize(): ackCh timeout ", e)
			return ErrTimeoutFlowControl
		}
	}

	return nil
}

// WriteFinal ensures that a frame with the Final bit set is sent, then
// calls Flush(). It's a protocol error to send frames after this call.
func (e *Exchange) WriteFinal() error {
	// log.Print("Exchange.WriteFinal() ", e)
	if e.hasSentFinal {
		return io.ErrClosedPipe
	}
	e.writeStart()
	e.fdw.Header().SetFinal()
	if err := e.Flush(); err != nil {
		FrameDataFree(e.fdw)
		e.fdw = nil
		return err
	}
	// wait for all sent frames to be acknowledged
	err := e.waitForSendWindowSize(SendWindowSize)
	e.sendWindow = SendWindowSize
	return err
}

// waitForFinalFrame discards incoming data until the final frame is received.
func (e *Exchange) waitForFinalFrame() (err error) {
	// log.Print("Exchange.waitForFinalFrame() ", e)
	for !e.hasReceivedFinal {
		if err = e.readFrame(); err != nil {
			// log.Print("Exchange.waitForFinalFrame(): readFrame() ", err.Error())
			break
		}
		FrameDataFree(e.fdr)
		e.fdr = nil
		e.fp = nil
	}
	return
}

// Close immediately closes an Exchange and frees any resouces.
// The Exchange may not be used after being closed.
func (e *Exchange) Close() (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeDoneChanLocked()
	return
}

// Stop returns an Exchange to it's initial state.
func (e *Exchange) Stop() (err error) {
	// log.Print("Exchange.Stop() ", e)
	if e.hasStarted {
		if !e.hasSentFinal {
			err = e.WriteFinal()
		}
		if finalErr := e.waitForFinalFrame(); err == nil {
			err = finalErr
		}
	}
	e.hasStarted = false
	e.hasReceived = false
	e.hasSentFinal = false
	e.hasReceivedFinal = false
	if e.sendWindow != SendWindowSize {
		e.sendWindow = SendWindowSize
		if err == nil {
			err = ErrTimeoutFlowControl
		}
	}
	return
}

// Release calls Stop() and then calls the releaser function.
func (e *Exchange) Release() {
	if e != nil {
		// log.Print("Exchange.Release() ", e)
		e.Stop()
		e.conn.ExchangeRelease(e)
	}
}

// WriteRequest writes a http.Request to the exchange, including it's Body and
// a final frame.
func (e *Exchange) WriteRequest(r *http.Request) (err error) {
	// log.Print("Exchange.WriteRequest() ", e)
	e.writeStart()

	if err = e.fdw.WriteRequest(r); err == nil {
		if _, err = e.ReadFrom(r.Body); err == nil {
			err = e.WriteFinal()
		}
	}

	return
}

// ProxyResponse reads a HTTP response and it's body from the Exchange data
// and writes it to the given http.ResponseWriter.
func (e *Exchange) ProxyResponse(w http.ResponseWriter) (err error) {
	// log.Print("Exchange.ProxyResponse() ", e)
	if err = e.loadFrameReader(); err != nil {
		return err
	}

	if !e.fdr.Header().HasHead() {
		return ErrMissingFrameHead
	}

	if e.fp.ReadRecordType() != RecordTypeHTTPResponse {
		return ErrUnhandledRecordType
	}

	e.fp.ProxyResponse(w)

	_, err = e.WriteTo(w)

	return
}

// WriteResponse writes a http.Response to the exchange.
func (e *Exchange) WriteResponse(r *http.Response) (err error) {
	// log.Print("Exchange.WriteResponse() ", e)
	return e.WriteResponseData(r.StatusCode, r.ContentLength, r.Header)
}

// WriteResponseData writes a RAP response header.
func (e *Exchange) WriteResponseData(code int, contentLength int64, header http.Header) (err error) {
	// log.Print("Exchange.WriteResponseData() ", e)
	e.writeStart()
	return e.fdw.WriteResponse(code, contentLength, header)
}

// Serve processes incoming RAP records until an error occurs.
func (e *Exchange) ServeHTTP(h http.Handler) (err error) {
	defer e.Release()
	for err == nil {
		err = e.Start(h)
		if err == nil {
			err = e.Stop()
		}
	}
	return
}

// Start waits for a start frame and then invokes the given http.Handler.
func (e *Exchange) Start(h http.Handler) error {
	// log.Print("Exchange.Start() ", e)
	if err := e.loadFrameReader(); err != nil {
		return err
	}
	if !e.fdr.Header().HasHead() {
		return ErrMissingFrameHead
	}
	e.hasStarted = true
	e.hasReceived = true
	switch e.fp.ReadRecordType() {
	case RecordTypeHTTPRequest:
		req, err := e.fp.ReadRequest()
		if err != nil {
			return err
		}
		req.Body = e
		h.ServeHTTP(&ResponseWriter{Exchange: e}, req)
	default:
		return ErrUnhandledRecordType
	}
	return nil
}
