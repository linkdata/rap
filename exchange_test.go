package rap

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

const leaktestEnabled = true

type exchangeTester struct {
	t           *testing.T
	wg          sync.WaitGroup
	once        sync.Once
	releasedCh  chan struct{}
	ackFn       func(*Exchange)
	finFn       func(*Exchange)
	conn        ExchangeConnection
	Exchange    *Exchange
	handler     http.Handler
	lastWritten FrameData
	sentFinal   int32
}

type exchangeTesterWriter struct {
	et          *exchangeTester
	writeCh     chan FrameData
	lastWritten FrameData
	closeCh     chan struct{}
	writeClosed int32
	timeoutErr  error
}

func newExchangeTesterWriter(et *exchangeTester) *exchangeTesterWriter {
	etw := &exchangeTesterWriter{
		et:         et,
		writeCh:    make(chan FrameData),
		closeCh:    make(chan struct{}),
		timeoutErr: errors.New("newExchangeTesterWriter timeout"),
	}

	go func() {
		timeout := time.NewTimer(time.Minute)
		defer timeout.Stop()
		for {
			var fd FrameData
			select {
			case fd = <-etw.writeCh:
			case <-timeout.C:
				log.Printf("%+v", etw.timeoutErr)
				assert.Fail(etw.et.t, "timeout")
			}
			if fd == nil {
				break
			}
			if !fd.Header().IsMuxerControl() {
				if fd.Header().HasFlow() {
					isFinal := fd.Header().IsFinal()
					needsAck := !fd.Header().IsFinalAck()
					FrameDataFree(fd)
					if isFinal {
						if etw.et.sendingFinal() {
							if etw.et.finFn != nil {
								etw.et.finFn(etw.et.Exchange)
							} else {
								if needsAck {
									etw.et.SubmitFrame(et.Exchange.makeFinalFrame(true))
								}
							}
						}
					}
				} else {
					etw.lastWritten = fd
					if etw.et.ackFn != nil {
						etw.et.ackFn(etw.et.Exchange)
					} else {
						etw.et.Exchange.ackCh <- struct{}{}
					}
				}
			}
		}
		close(etw.closeCh)
	}()

	return etw
}

func (etw *exchangeTesterWriter) isWriteClosed() bool {
	return atomic.LoadInt32(&etw.writeClosed) != 0
}

func (etw *exchangeTesterWriter) closingWrite() bool {
	return atomic.CompareAndSwapInt32(&etw.writeClosed, 0, 1)
}

func (etw *exchangeTesterWriter) CloseWrite() {
	if etw.closingWrite() {
		close(etw.writeCh)
	}
}

func (etw *exchangeTesterWriter) Close() {
	etw.CloseWrite()
}

func (etw *exchangeTesterWriter) WaitForClose() {
	timer := time.NewTimer(time.Second * 10)
	defer timer.Stop()
	select {
	case <-etw.closeCh:
	case <-timer.C:
		assert.Fail(etw.et.t, "exchangeTesterWriter timeout waiting for close")
	}
}

func (etw *exchangeTesterWriter) ExchangeWrite(fd FrameData) error {
	if !etw.et.isWriteClosed() {
		t := time.NewTimer(time.Second)
		defer t.Stop()
		select {
		case etw.writeCh <- fd:
		case <-etw.closeCh:
			return serverClosedError{}
		case <-t.C:
			log.Print("e: ", etw.et.Exchange)
			log.Print("fd: ", fd)
			panic("timeout waiting for ExchangeWrite to send")
		}
		return nil
	}
	return io.ErrClosedPipe
}

func (etw *exchangeTesterWriter) ExchangeRelease(e *Exchange) {
	etw.et.ExchangeRelease(e)
}

func (etw *exchangeTesterWriter) ExchangeAbortChannel() <-chan struct{} {
	return etw.closeCh
}

func newExchangeTester(t *testing.T) *exchangeTester {
	et := &exchangeTester{
		t:          t,
		releasedCh: make(chan struct{}),
	}
	et.conn = newExchangeTesterWriter(et)
	et.Exchange = NewExchange(et, MaxExchangeID)
	et.Exchange.OnRecycle(et.ExchangeRelease)
	et.wg.Add(1)
	return et
}

func newExchangeTesterUsingClient(t *testing.T, c *Client) *exchangeTester {
	exchange, e := c.NewExchangeMayDial()
	assert.NoError(t, e)
	assert.NotNil(t, exchange)
	assert.NotNil(t, exchange.conn)
	et := &exchangeTester{
		t:        t,
		conn:     exchange.conn,
		Exchange: exchange,
	}
	exchange.OnRecycle(et.ExchangeRelease)
	et.wg.Add(1)
	return et
}

func (et *exchangeTester) isWriteClosed() bool {
	if etw, ok := et.conn.(*exchangeTesterWriter); ok {
		return etw.isWriteClosed()
	}
	return false
}

func (et *exchangeTester) closeWrite() bool {
	if etw, ok := et.conn.(*exchangeTesterWriter); ok {
		return etw.closingWrite()
	}
	return false
}

func (et *exchangeTester) Released() bool {
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-et.releasedCh:
		return true
	case <-t.C:
		return false
	}
}

func (et *exchangeTester) CloseWrite() {
	if etw, ok := et.conn.(*exchangeTesterWriter); ok {
		etw.CloseWrite()
	}
}

func (et *exchangeTester) SubmitFrame(fd FrameData) error {
	et.Exchange.starting()
	return et.Exchange.SubmitFrame(fd)
}

func (et *exchangeTester) sendingFinal() bool {
	return atomic.CompareAndSwapInt32(&et.sentFinal, 0, 1)
}

func (et *exchangeTester) SendFinal() {
	if et.sendingFinal() {
		et.SubmitFrame(et.Exchange.makeFinalFrame(false))
	}
}

func (et *exchangeTester) ExchangeWrite(fd FrameData) error {
	if !et.isWriteClosed() {
		if fd != nil && fd.Header().HasPayload() {
			et.lastWritten = fd
		}
		return et.conn.ExchangeWrite(fd)
	}
	return io.ErrClosedPipe
}

func (et *exchangeTester) ExchangeRelease(e *Exchange) {
	et.once.Do(func() { close(et.releasedCh) })
}

func (et *exchangeTester) ExchangeAbortChannel() <-chan struct{} {
	return et.conn.ExchangeAbortChannel()
}

func (et *exchangeTester) Close() {
	et.Exchange.Close()
	if etw, ok := et.conn.(*exchangeTesterWriter); ok {
		etw.CloseWrite()
		etw.WaitForClose()
	}
}

func (et *exchangeTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

func (et *exchangeTester) InjectRequest(req *http.Request) {
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	err := fd.WriteRequest(req)
	assert.NoError(et.t, err)
	var buf bytes.Buffer
	n, err := io.Copy(&buf, req.Body)
	if err == nil && n > 0 {
		fd.Header().SetBody()
		fd.Header().SetSizeValue(int(n))
		precopylen := len(fd)
		n2, err2 := io.Copy(&fd, &buf)
		assert.Equal(et.t, precopylen+int(n2), len(fd))
		assert.NoError(et.t, err2)
		assert.Equal(et.t, n, n2)
	}
	et.SubmitFrame(fd)
	et.SendFinal()
}

type failWriterError struct {
	msg string // description of error
}

func (e *failWriterError) Error() string { return e.msg }

var errFailWriter = &failWriterError{msg: "failWriterError"}

type failWriter struct {
	byteCount     int
	failAtCount   int
	failOnClose   bool
	failOnWrite   bool
	failWithError error
	io.WriteCloser
}

func (fw *failWriter) Write(p []byte) (n int, err error) {
	if fw.failOnWrite {
		max := fw.failAtCount - fw.byteCount
		if max < 0 {
			max = 0
		}
		if len(p) > max {
			p = p[:max]
		}
	}
	if fw.WriteCloser == nil {
		err = fw.failError()
	} else {
		n, err = fw.WriteCloser.Write(p)
		fw.byteCount += n
		if err == nil && fw.failOnWrite && fw.byteCount >= fw.failAtCount {
			err = fw.failError()
		}
	}
	return
}

func (fw *failWriter) Close() (err error) {
	err = fw.WriteCloser.Close()
	if err == nil && fw.failOnClose {
		err = fw.failError()
	}
	return
}

func (fw *failWriter) failError() (err error) {
	if err = fw.failWithError; err == nil {
		err = errFailWriter
	}
	return
}

func Test_Exchange_String(t *testing.T) {
	e := NewExchange(&exchangeTester{}, 0x1)
	e.setRunState(runStateWaitFin)
	e.hijacking()
	e.localSendingFinal()
	e.remoteSendingFinal()
	expected := fmt.Sprintf("[Exchange %v %v   FIN   HJ LF RF (%d+%d)]", e.Serial(), e.ID, SendWindowSize, 0)
	assert.Equal(t, expected, e.String())
}

func Test_Exchange_Release(t *testing.T) {
	// Simple case
	et := newExchangeTester(t)
	defer et.Close()
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	et.Exchange.Serve(et)
	assert.True(t, et.Released())
}

func Test_Exchange_StartAndRelease_eof_before_starting(t *testing.T) {
	// EOF before starting
	et := newExchangeTester(t)
	defer et.Close()
	et.SendFinal()
	err := et.Exchange.Serve(et)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.True(t, et.Released())
}

func Test_Exchange_StartAndRelease_empty_frame(t *testing.T) {
	// Empty frame
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	et.SubmitFrame(fd)
	et.SendFinal()
	err := et.Exchange.Serve(et)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.True(t, et.Released())
}

func Test_Exchange_StartAndRelease_two_empty_frames(t *testing.T) {
	// Empty frame sequence
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.Header().SetHead()
	et.SubmitFrame(fd)
	fd = NewFrameDataID(MaxExchangeID)
	fd.Header().SetHead()
	et.SubmitFrame(fd)
	et.SendFinal()
	err := et.Exchange.Serve(et)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.True(t, et.Released())
}

func Test_Exchange_StartAndRelease_missing_frame_head(t *testing.T) {
	// Missing frame head
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetBody()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	et.SubmitFrame(fd)
	assert.Panics(t, func() { et.Exchange.Serve(et) })
	//err := et.Exchange.ServeHTTP(et)
	//assert.Equal(t, ErrMissingFrameHead, err)
	assert.True(t, et.Released())
}

func Test_Exchange_StartAndRelease_invalid_url_in_request_record(t *testing.T) {
	// Invalid URL in request record
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.WriteStringNull() // method
	fd.WriteStringNull() // scheme
	fd.WriteRoute(":a:") // illegal url
	et.SubmitFrame(fd)
	err := et.Exchange.Serve(et)
	assert.Error(t, err)
	assert.True(t, et.Released())
}

func Test_Exchange_StartAndRelease_incomplete_request_frame(t *testing.T) {
	// Incomplete request frame
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.WriteRecordType(RecordTypeHTTPRequest)
	et.SubmitFrame(fd)
	assert.Panics(t, func() {
		et.Exchange.Serve(et)
	})
	assert.True(t, et.Released())
}

func Test_Exchange_StartAndRelease_unhandled_record_type(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.WriteRecordType(RecordTypeUserFirst - 1)
	et.SubmitFrame(fd)
	err := et.Exchange.Serve(et)
	assert.Equal(t, ErrUnhandledRecordType{}, errors.Cause(err))
	assert.True(t, et.Released())
}

func Test_Exchange_StartAndRelease_missing_body_bit_panics(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	et.Exchange.writeStart()
	et.Exchange.fdw.Write(make([]byte, 1))
	var err error
	assert.Panics(t, func() { err = et.Exchange.Flush() })
	assert.NoError(t, err)
	err = et.Exchange.Close()
	assert.NoError(t, err)
	assert.True(t, et.Released())
}

func Test_Exchange_WriteByte(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	assert.Equal(t, ErrUnhandledRecordType{}, errors.Cause(et.Exchange.WriteUserRecordType(0x1)))

	assert.NoError(t, et.Exchange.WriteUserRecordType(0xFF))

	// HasBody is true after writing
	assert.NoError(t, et.Exchange.writeByte(0x01))
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+2, et.Exchange.Buffered())

	// Fill up to limit
	et.Exchange.write(make([]byte, et.Exchange.Available()))
	assert.Equal(t, FrameMaxSize, et.Exchange.Buffered())
	assert.True(t, et.Exchange.fdw.Header().HasBody())

	// Write one more should flush and start a new body
	assert.NoError(t, et.Exchange.writeByte(0x01))
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())
	assert.True(t, et.Exchange.fdw.Header().HasBody())

	// write final frame
	et.finFn = func(e *Exchange) {}
	et.Exchange.Close()

	// Flushing should fail since final is sent
	et.Exchange.write(make([]byte, et.Exchange.Available()))
	err := et.Exchange.WriteByte(0x01)
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(err))
	assert.True(t, et.Exchange.remoteSendingFinal())
	et.Exchange.recycle()
}

func Test_Exchange_Write(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	// Empty after writeStart()
	et.Exchange.writeStart()
	assert.False(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameMaxPayloadSize, et.Exchange.Available())

	assert.NoError(t, et.Exchange.WriteUserRecordType(0xFF))

	// HasBody is true after writing
	et.Exchange.writeByte(0x01)
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+2, et.Exchange.Buffered())

	// Fill up to just under limit
	et.Exchange.write(make([]byte, et.Exchange.Available()-1))
	assert.Equal(t, FrameMaxSize-1, et.Exchange.Buffered())
	assert.True(t, et.Exchange.fdw.Header().HasBody())

	// Write one more byte than can be fit
	et.Exchange.write([]byte{0x04, 0x05})
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())
}

func Test_Exchange_Flush(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	assert.NoError(t, et.Exchange.WriteUserRecordType(0xFF))

	// Normal flush
	n, err := et.Exchange.Write([]byte{0x01, 0x02})
	assert.NoError(t, err)
	assert.Equal(t, 2, n)
	err = et.Exchange.Flush()
	assert.NoError(t, err)

	// Flush after Flush is a no-op
	err = et.Exchange.Flush()
	assert.NoError(t, err)

	// Overflow the frame and flush should error
	assert.NoError(t, et.Exchange.WriteUserRecordType(0xFF))
	et.Exchange.fdw = append(et.Exchange.fdw, make([]byte, FrameMaxPayloadSize+1)...)
	et.Exchange.fdw.Header().SetBody()
	assert.Equal(t, FrameMaxSize+2, len(et.Exchange.fdw))
	err = et.Exchange.Flush()
	assert.Equal(t, ErrFrameTooBig{}, errors.Cause(err))
}

func Test_Exchange_Read(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0xc4)
	fd.Header().SetBody()
	et.SubmitFrame(fd)
	et.SendFinal()
	// Read the one-byte body
	p1 := make([]byte, 1)
	n, err := et.Exchange.Read(p1)
	assert.NoError(t, err)
	assert.Equal(t, len(p1), n)
	assert.Equal(t, byte(0xc4), p1[0])
	// Read again, expecting EOF
	n, err = et.Exchange.Read(p1)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.Zero(t, n)
}

func Test_Exchange_ReadFrom(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	assert.NoError(t, et.Exchange.WriteUserRecordType(0xFF))

	// Reading from nil
	n, err := et.Exchange.ReadFrom(nil)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.Zero(t, n)

	// Read one byte
	var buf bytes.Buffer
	buf.WriteByte(0xc4)
	n, err = et.Exchange.ReadFrom(&buf)
	assert.Equal(t, int64(1), n)
	assert.NoError(t, err)

	// Read more than max frame size bytes
	m, err := buf.Write(make([]byte, FrameMaxSize+1))
	assert.NoError(t, err)
	assert.Equal(t, FrameMaxSize+1, m)
	n, err = et.Exchange.ReadFrom(&buf)
	assert.Equal(t, int64(FrameMaxSize+1), n)
	assert.NoError(t, err)

	assert.NoError(t, et.Exchange.Flush())
	assert.Zero(t, len(et.Exchange.fdw))
	assert.False(t, et.Exchange.hasLocalSentFinal())

	m, err = buf.Write(make([]byte, FrameMaxSize*2+1))
	assert.NotZero(t, m)
	assert.NoError(t, err)
	et.Exchange.Close()
	n, err = et.Exchange.ReadFrom(&buf)
	assert.Error(t, io.ErrClosedPipe, errors.Cause(err))
}

func Test_Exchange_WriteTo(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0xc4)
	fd.Header().SetBody()
	et.SubmitFrame(fd)
	et.SendFinal()
	var buf bytes.Buffer
	n, err := et.Exchange.WriteTo(&buf)
	assert.Equal(t, int64(1), n)
	assert.NoError(t, err)
}

func Test_Exchange_WriteTo_FailWriter(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0xc5)
	fd.Header().SetBody()
	et.SubmitFrame(fd)
	n, err := et.Exchange.WriteTo(&failWriter{})
	assert.Equal(t, int64(0), n)
	assert.Error(t, err)
}

func Test_Exchange_WriteRequest(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	err := et.Exchange.WriteRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer([]byte{0xde, 0xad})))
	assert.False(t, et.Exchange.hasLocalSentFinal())
	assert.NoError(t, err)

	et = newExchangeTester(t)
	defer et.Close()
	et.finFn = func(e *Exchange) {} // ignore the final frame from Close()
	et.Exchange.starting()
	assert.NoError(t, et.Exchange.Close())
	assert.True(t, et.Exchange.hasLocalSentFinal())
	assert.False(t, et.Exchange.hasRemoteSentFinal())
	err = et.Exchange.WriteRequest(httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(err))
}

func Test_Exchange_WriteResponse(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	rr := httptest.NewRecorder()
	rr.WriteString("Meh")
	rr.WriteHeader(200)
	err := et.Exchange.WriteResponse(rr.Result())
	assert.False(t, et.Exchange.hasLocalSentFinal())
	assert.NoError(t, err)
}

func Test_Exchange_CloseWrite(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	et.Exchange.starting()
	close(et.Exchange.localClosed)
	err := et.Exchange.Close()
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(err))

	et = newExchangeTester(t)
	defer et.Close()
	et.Exchange.writeStart()
	et.Exchange.fdw.Write(make([]byte, FrameMaxSize+1))
	et.Exchange.fdw.Header().SetBody()
	err = et.Exchange.Flush()
	assert.Equal(t, ErrFrameTooBig{}, errors.Cause(err))
	err = et.Exchange.Close()
	assert.NoError(t, err)

	et = newExchangeTester(t)
	defer et.Close()
	assert.NoError(t, et.Exchange.WriteUserRecordType(0xFF))
	assert.NoError(t, et.Exchange.WriteByte(0x01))
	assert.NoError(t, et.Exchange.Flush())
	assert.NoError(t, et.Exchange.WriteByte(0x02))
	assert.NoError(t, et.Exchange.Flush())
	et.SendFinal()
	err = et.Exchange.loadFrameReader()
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.Zero(t, len(et.Exchange.readCh))
	assert.NoError(t, et.Exchange.Close())
	assert.True(t, et.Released())
}

func Test_Exchange_ProxyResponse_transparency(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	// Make a frame for testing with
	et := newExchangeTester(t)
	defer et.Close()
	rr := httptest.NewRecorder()
	rr.WriteHeader(201)
	_, err := rr.WriteString("Meh")
	assert.NoError(t, err)
	assert.NoError(t, et.Exchange.WriteResponse(rr.Result()))
	assert.NoError(t, et.Exchange.Close())
	assert.True(t, et.Released())

	testingFrame := et.lastWritten
	assert.NotNil(t, testingFrame)
	assert.True(t, testingFrame.Header().HasPayload())
	assert.Equal(t, 9, testingFrame.Header().PayloadSize())
	assert.Equal(t, 201, rr.Code)
	assert.Equal(t, int(3), rr.Body.Len())

	// n, err := et.Exchange.ReadFrom(rr.Body)
	// assert.NoError(t, err)
	// assert.Equal(t, int64(3), n)

	assert.NoError(t, et.Exchange.Close())

	// Test transparency
	et2 := newExchangeTester(t)
	defer et2.Close()
	et2.SubmitFrame(testingFrame)
	et2.SendFinal()
	rr2 := httptest.NewRecorder()
	_, err = et2.Exchange.ProxyResponse(rr2)
	assert.NoError(t, err)
	_, err = et2.Exchange.WriteTo(rr2)
	assert.Error(t, io.EOF, errors.Cause(err))
	assert.True(t, et2.Exchange.hasRemoteSentFinal())
	assert.Equal(t, int(3), rr.Body.Len())
	assert.Equal(t, int(3), rr2.Body.Len())
	assert.Equal(t, rr.Body.String(), rr2.Body.String())
	assert.Equal(t, rr.Header(), rr2.Header())
	compareResponse(t, rr.Result(), rr2.Result())
}

func compareResponse(t *testing.T, r1, r2 *http.Response) {
	assert.Equal(t, r1.Close, r2.Close)
	assert.Equal(t, r1.ContentLength, r2.ContentLength)
	assert.Equal(t, r1.Cookies(), r2.Cookies())
	assert.Equal(t, r1.Header, r2.Header)
	u1, _ := r1.Location()
	u2, _ := r2.Location()
	assert.Equal(t, u1, u2)
	assert.Equal(t, r1.Proto, r2.Proto)
	assert.Equal(t, r1.ProtoMajor, r2.ProtoMajor)
	assert.Equal(t, r1.ProtoMinor, r2.ProtoMinor)
	assert.Equal(t, r1.Status, r2.Status)
	assert.Equal(t, r1.StatusCode, r2.StatusCode)
	assert.Equal(t, r1.Trailer, r2.Trailer)
	assert.Equal(t, r1.TransferEncoding, r2.TransferEncoding)
	assert.Equal(t, r1.Uncompressed, r2.Uncompressed)
}

func Test_Exchange_ProxyResponse_read_eof(t *testing.T) {
	// Test read error
	et := newExchangeTester(t)
	defer et.Close()
	et.SendFinal()
	rr2 := httptest.NewRecorder()
	_, err := et.Exchange.ProxyResponse(rr2)
	assert.True(t, et.Exchange.hasRemoteSentFinal())
	assert.Equal(t, io.EOF, errors.Cause(err))
}

/*
func Test_Exchange_ProxyResponse_frame_missing_head(t *testing.T) {
	// Test frame missing head
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0x01)
	fd.Header().SetBody()
	et.SubmitFrame(fd)
	et.SendFinal()
	rr2 := httptest.NewRecorder()
	_, err := et.Exchange.ProxyResponse(rr2)
	assert.True(t, et.Exchange.hasReceivedFinal())
	assert.Equal(t, ErrMissingFrameHead, err)
}
*/

func Test_Exchange_ProxyResponse_wrong_record_type(t *testing.T) {
	// Test wrong record type
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteRecordType(RecordTypeUserFirst)
	et.SubmitFrame(fd)
	et.SendFinal()
	rr2 := httptest.NewRecorder()
	_, err := et.Exchange.ProxyResponse(rr2)
	assert.True(t, et.Exchange.hasRemoteSentFinal())
	assert.Equal(t, ErrUnhandledRecordType{}.Error(), errors.Cause(err).Error())
}

func Test_Exchange_Close(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.Header().SetBody()
	et.SubmitFrame(fd)
	err := et.Exchange.Close()
	assert.NoError(t, err)
}

func Test_Exchange_Stop(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	assert.NoError(t, et.Exchange.Serve(et))
	assert.NoError(t, et.Exchange.Close())
	assert.True(t, et.Released())
}

func Test_Exchange_Serve(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		et.Exchange.Serve(et)
		wg.Done()
	}()
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	et.SendFinal()
	wg.Wait()
}

func Test_Exchange_flowcontrol_errors(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	et := newExchangeTester(t)
	defer et.Close()
	et.ackFn = func(e *Exchange) {
		if e.getSendWindow() == 0 {
			e.SetWriteDeadline(time.Now().Add(time.Millisecond * 10))
		}
	}
	err := et.Exchange.WriteRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, FrameMaxPayloadSize*(MaxSendWindowSize+1)))))
	assert.Error(t, err)
	nerr, ok := errors.Cause(err).(net.Error)
	assert.True(t, ok)
	if ok {
		if !nerr.Timeout() {
			assert.NoError(t, nerr)
		}
		if !nerr.Temporary() {
			assert.NoError(t, nerr)
		}
	} else {
		// expected a net.Error
		assert.NoError(t, err)
	}
	assert.Zero(t, len(et.Exchange.ackCh))
	assert.Zero(t, et.Exchange.getSendWindow())
	et.Exchange.sendWindow = int32(SendWindowSize)
}

func Test_Exchange_SetDeadline_on_closed(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	et.finFn = func(e *Exchange) {}
	et.Exchange.starting()
	et.Exchange.Close()
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(et.Exchange.SetDeadline(time.Now())))
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(et.Exchange.SetReadDeadline(time.Now())))
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(et.Exchange.SetWriteDeadline(time.Now())))
	assert.True(t, et.Exchange.remoteSendingFinal())
	et.Exchange.recycle()
}

func makeConnPipe() (e1, e2 net.Conn, stop func(), err error) {
	return makeExchangePipe()
}

func makeExchangePipe() (e1, e2 *Exchange, stop func(), err error) {
	p1, p2 := net.Pipe()

	wg := sync.WaitGroup{}

	c1 := NewMuxer(p1)
	wg.Add(2)
	go func() { defer wg.Done(); c1.WriteTo(p1) }()
	go func() { defer wg.Done(); c1.ReadFrom(p1) }()

	c2 := NewMuxer(p2)
	wg.Add(2)
	go func() { defer wg.Done(); c2.WriteTo(p2) }()
	go func() { defer wg.Done(); c2.ReadFrom(p2) }()

	e1 = c1.NewExchange()
	e2 = c2.exchangeLookup[e1.ID]

	return e1, e2, func() {
		e1.Close()
		e2.Close()
		c2.Close()
		c1.Close()
		p1.Close()
		p2.Close()
		wg.Wait()
	}, nil
}

func Test_Exchange_makeConnPipe(t *testing.T) {
	c1, c2, stop, err := makeConnPipe()
	defer stop()
	assert.NoError(t, err)
	assert.NotNil(t, c1)
	assert.NotNil(t, c2)
	assert.NotNil(t, stop)
}

func Test_Exchange_flowcontrol_halts(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	c1, c2, stop, err := makeExchangePipe()
	defer stop()
	assert.NoError(t, err)
	assert.NotNil(t, c1)
	assert.NotNil(t, c2)
	c2.WriteUserRecordType(0xFF)
	/*
		c2 being written to is streamed to c1, which is not ACK'ing to c2
		since there is no one reading from it.
	*/
	for i := 1; i <= SendWindowSize; i++ {
		c2.Write([]byte{1})
		assert.Equal(t, SendWindowSize-i, c2.getSendWindow())
	}
	// This write should time out
	c2.SetWriteDeadline(time.Now().Add(time.Millisecond * 10))
	n, err := c2.Write([]byte{1})
	assert.Zero(t, n)
	assert.Equal(t, timeoutError{}.Error(), errors.Cause(err).Error())
	c2.SetWriteDeadline(time.Time{})
	// Read a byte from c1, should free up window space
	b1 := make([]byte, 1)
	n, err = c1.Read(b1)
	assert.Equal(t, 1, n)
	assert.NoError(t, err)
	// Now the write to c2 should be OK
	n, err = c2.Write([]byte{1})
	assert.Equal(t, 1, n)
	assert.NoError(t, err)

	// closing C1 discards all the pending data
	assert.NoError(t, c1.Close())

	// wait for remote to be closed
	for !c2.hasRemoteSentFinal() {
		time.Sleep(time.Millisecond)
	}

	// should fail with EOF indicating remote closed
	n, err = c2.Write([]byte{1})
	assert.Equal(t, 0, n)
	assert.Equal(t, io.EOF, errors.Cause(err))

	assert.NoError(t, c2.Close())
}

func Test_Exchange_transparency(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	testConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		c1, c2 = net.Pipe()
		stop = func() {
			c1.Close()
			c2.Close()
		}
		return
	})
	testConn(t, makeConnPipe)
}

func Test_Exchange_Addr_interface(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	la := et.Exchange.LocalAddr()
	assert.NotNil(t, la)
	assert.Equal(t, "rap", la.Network())
	assert.Equal(t, "rap", la.String())

	ra := et.Exchange.RemoteAddr()
	assert.NotNil(t, ra)
	assert.Equal(t, "rap", ra.Network())
	assert.Equal(t, "rap", ra.String())
}

func Test_Exchange_final_frame_with_body_panics(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	fd := et.Exchange.makeFinalFrame(false)
	fd.WriteByte(0)
	fd.SetSizeValue()

	assert.Panics(t, func() {
		et.Exchange.SubmitFrame(fd)
	})
}

func Test_Exchange_multiple_final_frames_panics(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	fd := et.Exchange.makeFinalFrame(false)
	err := et.SubmitFrame(fd)
	assert.NoError(t, err)

	fd = et.Exchange.makeFinalFrame(false)
	assert.Panics(t, func() {
		et.SubmitFrame(fd)
	})
}

func Test_Exchange_recycle_with_only_local_closed_panics(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	et.Exchange.starting()
	close(et.Exchange.localClosed)

	assert.Panics(t, func() {
		et.Exchange.recycle()
	})

	assert.True(t, et.Exchange.remoteSendingFinal())

}

func Test_Exchange_manually_sending_final_frame_panics(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	et.Exchange.writeByte(0)
	et.Exchange.fdw.Header().SetFlow()

	assert.Panics(t, func() {
		et.Exchange.Flush()
	})
}
