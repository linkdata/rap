package rap

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime/debug"
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

// Handler is the RAP handler interface for handling an exchange.
type Handler interface {
	http.Handler
}

// Exchange maintains the state of a request-response or WebSocket connection.
// It also handles the flow control mechanism, which is a simple transmission
// window with intermittent ACKs from the receiver.
type Exchange struct {
	ID               ExchangeID // Exchange ID
	Conn             *Conn
	readCh           chan FrameData
	ackCh            chan struct{}
	sendWindow       int         // number of frames still allowed to be in flight
	fdw              FrameData   // FrameData being written to, nil after final frame sent
	fdr              FrameData   // FrameData being read from by fr
	fr               FrameReader // Frame payload reader (into fdr)
	hasStarted       bool        // true if the exchange has sent or received the first frame
	hasSentClose     bool        // true if we have sent our final frame
	hasReceivedClose bool        // true if the peer has sent a it's final frame
}

func (e *Exchange) String() string {
	return fmt.Sprintf("[Exchange %v sendW=%v started=%v sentC=%v recvC=%v len(readCh)=%d len(ackCh)=%d]",
		e.ID, e.sendWindow, e.hasStarted, e.hasSentClose, e.hasReceivedClose, len(e.readCh), len(e.ackCh))
}

// NewExchange creates a new exchange state on the given Conn.
func NewExchange(conn *Conn, exchangeID ExchangeID) *Exchange {
	return &Exchange{
		ID:         exchangeID,
		Conn:       conn,
		sendWindow: SendWindowSize,
		readCh:     make(chan FrameData, MaxSendWindowSize),
		ackCh:      make(chan struct{}, MaxSendWindowSize),
	}
}

// WriteFrameData queues a FrameData for writing
func (e *Exchange) writeFrameX(fd FrameData) {
	// set the payload size
	if fd.Header().HasPayload() {
		fd.Header().SetSizeValue(int32(len(fd)) - FrameHeaderSize)
	}
	select {
	case e.Conn.writeCh <- fd:
		return
	case _, ok := <-e.ackCh:
		if ok {
			e.sendWindow++
		}
	}
}

// readFrame reads data frames from the read channel.
// None of the frames seen may be conn control frames.
func (e *Exchange) readFrame() error {
	// log.Print("Exchange.readFrame(): len(fr)=", len(e.fr), " e=", e)
	if e.fdr != nil {
		FrameDataFree(e.fdr)
		e.fdr = nil
		e.fr = nil
	}

	if e.hasReceivedClose {
		// log.Print("Exchange.readFrame(): EOF e=", e)
		return io.EOF
	}

	if e.fdr = <-e.readCh; e.fdr == nil {
		return io.EOF
	}

	e.fr = NewFrameReader(e.fdr)
	if e.fdr.Header().IsFinal() {
		e.hasReceivedClose = true
	} else {
		fda := FrameDataAlloc()
		fda.WriteHeader(e.ID)
		e.Conn.writeCh <- fda
	}

	return nil
}

// loadFrameReader ensures the frame reader has payload data or an error.
func (e *Exchange) loadFrameReader() (err error) {
	// log.Print("Exchange.loadFrameReader(): ", e.ID, " len(fr)=", len(e.fr))
	for len(e.fr) == 0 && err == nil {
		err = e.readFrame()
		// log.Print("Exchange.loadFrameReader(): ", e.ID, " readFrame() len(fr)=", len(e.fr), " err=", err, "  ", e)
	}
	return
}

// Start waits for a start frame and then invokes the handler.
func (e *Exchange) Start(h Handler) error {
	// log.Print("Exchange.Start() ", e)
	if err := e.loadFrameReader(); err != nil {
		return err
	}
	if !e.fdr.Header().HasHead() {
		return ErrMissingFrameHead
	}
	e.hasStarted = true
	switch e.fr.ReadRecordType() {
	case RecordTypeHTTPRequest:
		req, err := e.fr.ReadRequest()
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

// ProxyResponse reads a HTTP response and it's body from the Exchange data
// and writes it to the given http.ResponseWriter.
func (e *Exchange) ProxyResponse(w http.ResponseWriter) error {
	// log.Print("Exchange.ProxyResponse() ", e)
	if err := e.loadFrameReader(); err != nil {
		return err
	}

	if !e.fdr.Header().HasHead() {
		return ErrMissingFrameHead
	}

	if e.fr.ReadRecordType() != RecordTypeHTTPResponse {
		return ErrUnhandledRecordType
	}

	e.fr.ProxyResponse(w)

	if _, err := e.WriteTo(w); err != nil {
		return err
	}

	return nil
}

func (e *Exchange) Read(p []byte) (n int, err error) {
	// log.Print("Exchange.Read([", len(p), "]) ", e)
	if err = e.loadFrameReader(); err != nil {
		// log.Print("Exchange.Read([", len(p), "]) => n=0 err=", err.Error())
		return
	}
	// log.Print("Exchange.Read([", len(p), "]) calling e.fr.Read() len=", len(e.fr), " ", e)
	n, err = e.fr.Read(p)
	// log.Print("Exchange.Read([", len(p), "]) Read() => len(e.fr)=", len(e.fr), " n=", n, " err=", err)
	if err == nil && e.hasReceivedClose {
		err = io.EOF
	}
	// log.Print("Exchange.Read([", len(p), "]) => n=", n, " err=", err)
	return
}

// writeStart prepares a new frame for writing.
func (e *Exchange) writeStart() {
	if e.fdw == nil {
		// log.Print("Exchange.writeStart() (new fd)", e)
		e.fdw = FrameDataAlloc()
		e.fdw.WriteHeader(e.ID)
	}
}

// WriteRequest writes a http.Request to the exchange, including it's Body and
// a final frame.
func (e *Exchange) WriteRequest(r *http.Request) error {
	// log.Print("Exchange.WriteRequest() ", e)
	e.writeStart()

	if err := e.fdw.WriteRequest(r); err != nil {
		log.Print("Exchange.WriteRequest(): fdw.WriteRequest: ", err.Error(), " e.fdw=", e.fdw)
		return err
	}

	if _, err := e.ReadFrom(r.Body); err != nil {
		log.Print("Exchange.WriteRequest(): ReadFrom(r.Body): ", err.Error())
		return err
	}

	if err := e.CloseWrite(); err != nil {
		log.Print("Exchange.WriteRequest(): CloseWrite: ", err.Error())
		return err
	}

	return nil
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
	if err := e.fdw.WriteResponse(code, contentLength, header); err != nil {
		return err
	}
	return nil
}

// Available returns number of free bytes in the current frame.
func (e *Exchange) Available() int {
	return e.fdw.Available()
}

// Buffered returns the number of bytes that have been written to the current frame.
func (e *Exchange) Buffered() int {
	return e.fdw.Buffered()
}

// ReadFrom implements io.ReaderFrom for Exchange body data.
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
	if err == io.EOF {
		err = nil
	}
	return n, nil
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
		for len(e.fr) > 0 {
			var count int
			count, err = w.Write(e.fr)
			e.fr = e.fr[count:]
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
	}
	return e.fdw.WriteByte(c)
}

// TODO: func (b *Writer) WriteRune(r rune) (size int, err error)
// TODO: func (b *Writer) WriteString(s string) (int, error)

// Flush handles write flow control and injects the current frame into the conn.
// Note that the current write frame is expected to be a regular data frame,
// such that e.fdw.Header() returns false for IsConnControl() and true for
// HasPayload().
func (e *Exchange) Flush() error {
	// log.Print("Exchange.Flush() ", e)
	if e.hasSentClose {
		return io.ErrClosedPipe
	}

	if e.fdw == nil {
		return nil
	}

	if len(e.fdw) > FrameMaxSize {
		e.fdw = nil
		e.sendClose()
		return ErrFrameTooBig
	}

	e.hasStarted = true

	if e.fdw.Header().IsFinal() {
		// final frame is not acked
		e.hasSentClose = true
	} else {
		// make sure window allows us to send
		if e.sendWindow < 1 {
		loopAcks:
			for {
				select {
				case _, ok := <-e.ackCh:
					if !ok {
						log.Print("Exchange.Flush(): ackCh closed ", e)
						e.sendClose()
						return io.ErrClosedPipe
					}
					e.sendWindow++
				default:
					break loopAcks
				}
			}

			for e.sendWindow < 1 {
				timer := time.NewTimer(WriteTimeout)
				defer timer.Stop()
				// log.Print("Exchange.Flush(): starting wait ", e)
				select {
				case _, ok := <-e.ackCh:
					e.sendWindow++ // didn't actuall
					if !ok {
						log.Print("Exchange.Flush(): ackCh closed ", e)
						e.sendClose()
						return io.ErrClosedPipe
					}
				case <-timer.C:
					e.sendWindow++
					log.Print("Exchange.Flush(): ackCh timeout ", e)
					return ErrTimeoutFlowControl
				}
				// log.Print("Exchange.Flush(): finished wait ", e)
			}
		}
		e.sendWindow--
	}

	if e.fdw.Header().HasPayload() {
		e.fdw.Header().SetSizeValue(int32(len(e.fdw)) - FrameHeaderSize)
	}

	e.Conn.writeCh <- e.fdw
	e.fdw = nil
	return nil
}

func (e *Exchange) sendClose() {
	if e.hasStarted && !e.hasSentClose {
		e.hasSentClose = true
		fdc := FrameDataAlloc()
		fdc.Header().SetExchangeID(e.ID)
		fdc.Header().SetFinal()
		e.Conn.writeCh <- fdc
		if e.fdw != nil {
			FrameDataFree(e.fdw)
			e.fdw = nil
		}
	}
}

// CloseWrite ensures that a frame with the Final bit set is sent, then
// calls Flush(). It's a protocol error to send frames after this call.
func (e *Exchange) CloseWrite() error {
	// log.Print("Exchange.CloseWrite() ", e)
	if e.hasSentClose {
		return io.ErrClosedPipe
	}
	e.writeStart()
	e.fdw.Header().SetFinal()
	if err := e.Flush(); err != nil {
		if e.fdw != nil {
			FrameDataFree(e.fdw)
			e.fdw = nil
		}
		return err
	}
	// wait for all sent frames to be acknowledged
	for e.sendWindow < SendWindowSize {
		timer := time.NewTimer(WriteTimeout)
		defer timer.Stop()
		// log.Print("Exchange.Flush(): starting wait ", e)
		select {
		case _, ok := <-e.ackCh:
			e.sendWindow++
			if !ok {
				log.Print("Exchange.CloseWrite(): ackCh closed ", e)
				e.sendWindow = SendWindowSize
				return io.ErrClosedPipe
			}
		case <-timer.C:
			log.Print("Exchange.CloseWrite(): ackCh timeout ", e)
			e.sendWindow = SendWindowSize
			return ErrTimeoutFlowControl
		}
	}
	return nil
}

// Close discards incoming data until the final frame is received.
func (e *Exchange) Close() (err error) {
	// log.Print("Exchange.Close() ", e)
	for !e.hasReceivedClose {
		if err = e.readFrame(); err != nil {
			// log.Print("Exchange.Close(): readFrame() ", err.Error())
			break
		}
		FrameDataFree(e.fdr)
		e.fdr = nil
		e.fr = nil
	}
	return
}

// Stop returns an Exchange to it's initial state.
func (e *Exchange) Stop() (err error) {
	// log.Print("Exchange.Stop() ", e)
	if e.hasStarted {
		if !e.hasSentClose {
			err = e.CloseWrite()
		}
		if closeErr := e.Close(); err == nil {
			err = closeErr
		}
	}
	e.hasStarted = false
	e.hasSentClose = false
	e.hasReceivedClose = false
	if e.sendWindow != SendWindowSize {
		log.Print("Exchange.Stop() sendWindow=", e.sendWindow)
		e.sendWindow = SendWindowSize
	}
	return
}

// Release calls Stop() and then returns an Exchange to the conn it belongs.
func (e *Exchange) Release() {
	// log.Print("Exchange.Release() ", e)
	e.Stop()
	e.Conn.releaseExchange(e)
}

func serveHTTP(h http.Handler, rw *ResponseWriter, req *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Print("Exchange.serveHTTP(): ", r)
			debug.PrintStack()
		}
	}()
	h.ServeHTTP(rw, req)
}

func (e *Exchange) serve(h http.Handler) {
	defer e.Release()
	for {
		if err := e.Start(h); err != nil {
			if err != io.EOF {
				log.Print("Exchange.serve() ", e.ID, ": ", err)
			}
			return
		}
		e.Stop()
	}
}
