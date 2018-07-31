package rap

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/stretchr/testify/assert"
)

type exchangeTester struct {
	t           *testing.T
	wg          sync.WaitGroup
	released    bool
	ackClosed   bool
	doneClosed  bool
	writeClosed bool
	readClosed  bool
	conn        ExchangeConnection
	Exchange    *Exchange
	handler     http.Handler
}

type exchangeTesterWriter struct {
	et          *exchangeTester
	writeCh     chan FrameData
	lastWritten FrameData
}

func newExchangeTesterWriter(et *exchangeTester) *exchangeTesterWriter {
	etw := &exchangeTesterWriter{
		et:      et,
		writeCh: make(chan FrameData),
	}

	go func() {
		for {
			fd := <-etw.writeCh
			if fd == nil {
				break
			}
			etw.lastWritten = fd
		}
	}()

	return etw
}

func (etw *exchangeTesterWriter) ExchangeWrite(fd FrameData) error {
	if !etw.et.writeClosed {
		etw.writeCh <- fd
		return nil
	}
	return io.ErrClosedPipe
}

func (etw *exchangeTesterWriter) ExchangeRelease(e *Exchange) {
	etw.et.released = true
}

func (etw *exchangeTesterWriter) ExchangeTimeout() time.Duration {
	return time.Millisecond * 100
}

func newExchangeTester(t *testing.T) *exchangeTester {
	et := &exchangeTester{
		t: t,
	}
	et.conn = newExchangeTesterWriter(et)
	et.Exchange = NewExchange(et, MaxExchangeID)
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
	et.wg.Add(1)
	return et
}

func (et *exchangeTester) CloseWrite() {
	if !et.writeClosed {
		et.writeClosed = true
		if etw, ok := et.conn.(*exchangeTesterWriter); ok {
			close(etw.writeCh)
		}
	}
}

func (et *exchangeTester) SubmitFrame(fd FrameData) error {
	return et.Exchange.SubmitFrame(fd)
}

func (et *exchangeTester) ExchangeWrite(fd FrameData) error {
	if !et.writeClosed {
		return et.conn.ExchangeWrite(fd)
	}
	return io.ErrClosedPipe
}

func (et *exchangeTester) ExchangeRelease(e *Exchange) {
	et.released = true
	et.conn.ExchangeRelease(e)
}

func (et *exchangeTester) ExchangeTimeout() time.Duration {
	return et.conn.ExchangeTimeout()
}

func (et *exchangeTester) Close() {
	et.Exchange.Close()
	et.CloseWrite()
}

func (et *exchangeTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

func (et *exchangeTester) InjectRequest(req *http.Request) {
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetFinal()
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
	assert.Equal(t, "[Exchange [ExchangeID 0001] sendW=8 started=false sentC=false recvC=false len(ackCh)=0]", e.String())
}

func Test_Exchange_StartAndRelease_simple(t *testing.T) {
	// Simple case
	et := newExchangeTester(t)
	defer et.Close()
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	et.Exchange.Start(et)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_StartAndRelease_eof_before_starting(t *testing.T) {
	// EOF before starting
	et := newExchangeTester(t)
	defer et.Close()
	et.SubmitFrame(nil)
	err := et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_StartAndRelease_empty_frame(t *testing.T) {
	// Empty frame
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	err := et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_StartAndRelease_two_empty_frames(t *testing.T) {
	// Empty frame sequence
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	et.SubmitFrame(fd)
	fd = NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	err := et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_StartAndRelease_missing_frame_head(t *testing.T) {
	// Missing frame head
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetBody()
	fd.Header().SetFinal()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	et.SubmitFrame(fd)
	err := et.Exchange.Start(et)
	assert.Equal(t, ErrMissingFrameHead, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_StartAndRelease_invalid_url_in_request_record(t *testing.T) {
	// Invalid URL in request record
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.WriteStringNull()  // method
	fd.WriteString(":a:") // illegal url
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	err := et.Exchange.Start(et)
	assert.Error(t, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_StartAndRelease_incomplete_request_frame(t *testing.T) {
	// Incomplete request frame
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	fd.Header().SetFinal()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	et.SubmitFrame(fd)
	assert.Panics(t, func() {
		et.Exchange.Start(et)
	})
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_StartAndRelease_illegal_record_type(t *testing.T) {
	// Illegal record type
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeUserFirst - 1)
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	err := et.Exchange.Start(et)
	assert.Equal(t, ErrUnhandledRecordType, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_SubmitFrame_close_during_ack_read(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetFinal()
	et.Exchange.Close()
fillAckCh:
	for {
		select {
		case et.Exchange.ackCh <- struct{}{}:
		default:
			break fillAckCh
		}
	}
	et.SubmitFrame(fd)
	err := et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	// A Close()'d Exchange must not be released
	assert.False(t, et.released)
}

func Test_Exchange_SubmitFrame_close_during_data_read(t *testing.T) {
	defer leaktest.Check(t)()
	et := newExchangeTester(t)
	defer et.Close()
	et.Exchange.Close()
waitForReaderToStop:
	for {
		select {
		case et.Exchange.readCh <- nil:
		default:
			break waitForReaderToStop
		}
	}
	err := et.SubmitFrame(nil)
	assert.Equal(t, io.ErrClosedPipe, err)
	et.Exchange.Release()
	assert.False(t, et.released)
}

func Test_Exchange_WriteByte(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	// HasBody is true after writing
	et.Exchange.WriteByte(0x01)
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())

	// Fill up to limit
	et.Exchange.fdw.Header().SetFinal()
	et.Exchange.Write(make([]byte, et.Exchange.Available()))
	assert.Equal(t, FrameMaxSize, et.Exchange.Buffered())
	assert.True(t, et.Exchange.fdw.Header().HasBody())

	// Write one more should flush
	assert.NoError(t, et.Exchange.WriteByte(0x01))
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())

	// Force a flush, should fail since final is sent
	n, err := et.Exchange.Write(make([]byte, et.Exchange.Available()))
	assert.NoError(t, err)
	assert.NotZero(t, n)
	err = et.Exchange.WriteByte(0x01)
	assert.Equal(t, io.ErrClosedPipe, err)
}

func Test_Exchange_Write(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	// Empty after writeStart()
	et.Exchange.writeStart()
	assert.False(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameMaxPayloadSize, et.Exchange.Available())

	// HasBody is true after writing
	et.Exchange.WriteByte(0x01)
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())

	// Fill up to just under limit
	et.Exchange.Write(make([]byte, et.Exchange.Available()-1))
	assert.Equal(t, FrameMaxSize-1, et.Exchange.Buffered())
	assert.True(t, et.Exchange.fdw.Header().HasBody())

	// Set final and write one more byte than can be fit
	et.Exchange.fdw.Header().SetFinal()
	et.Exchange.Write([]byte{0x04, 0x05})
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())

	// Force a flush, should fail since final is sent
	n, err := et.Exchange.Write(make([]byte, et.Exchange.Available()+1))
	assert.Equal(t, io.ErrClosedPipe, err)
	assert.NotZero(t, n)
}

func Test_Exchange_Flush(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

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
	et.Exchange.writeStart()
	et.Exchange.fdw = append(et.Exchange.fdw, make([]byte, FrameMaxPayloadSize+1)...)
	assert.Equal(t, FrameMaxSize+1, len(et.Exchange.fdw))
	err = et.Exchange.Flush()
	assert.Equal(t, ErrFrameTooBig, err)
}

func Test_Exchange_Read(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0xc4)
	fd.Header().SetBody()
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	// Read the one-byte body
	p1 := make([]byte, 1)
	n, err := et.Exchange.Read(p1)
	assert.NoError(t, err)
	assert.Equal(t, len(p1), n)
	assert.Equal(t, byte(0xc4), p1[0])
	// Read again, expecting EOF
	n, err = et.Exchange.Read(p1)
	assert.Equal(t, io.EOF, err)
	assert.Zero(t, n)
}

func Test_Exchange_ReadFrom(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()

	// Reading from nil
	n, err := et.Exchange.ReadFrom(nil)
	assert.Equal(t, io.EOF, err)
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

	// Again, but have send a final frame and Flush()
	// to force the write to return an error
	et.Exchange.fdw.Header().SetFinal()
	assert.NoError(t, et.Exchange.Flush())
	assert.Zero(t, len(et.Exchange.fdw))
	m, err = buf.Write(make([]byte, FrameMaxSize*2+1))
	assert.NotZero(t, m)
	assert.NoError(t, err)
	n, err = et.Exchange.ReadFrom(&buf)
	assert.Equal(t, int64(FrameMaxPayloadSize), n)
	assert.Error(t, io.ErrClosedPipe, err)
}

func Test_Exchange_WriteTo(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0xc4)
	fd.Header().SetBody()
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
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
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	n, err := et.Exchange.WriteTo(&failWriter{})
	assert.Equal(t, int64(0), n)
	assert.Error(t, err)
}

func Test_Exchange_WriteRequest(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	err := et.Exchange.WriteRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer([]byte{0xde, 0xad})))
	assert.NoError(t, err)

	et = newExchangeTester(t)
	defer et.Close()
	assert.NoError(t, et.Exchange.WriteFinal())
	err = et.Exchange.WriteRequest(httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, io.ErrClosedPipe, err)
}

func Test_Exchange_WriteResponse(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	rr := httptest.NewRecorder()
	rr.WriteString("Meh")
	rr.WriteHeader(200)
	err := et.Exchange.WriteResponse(rr.Result())
	assert.NoError(t, err)
}

func Test_Exchange_CloseWrite(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	err := et.Exchange.WriteFinal()
	assert.NoError(t, err)
	err = et.Exchange.WriteFinal()
	assert.Equal(t, io.ErrClosedPipe, err)

	et = newExchangeTester(t)
	defer et.Close()
	et.Exchange.writeStart()
	et.Exchange.fdw.Write(make([]byte, FrameMaxSize+1))
	err = et.Exchange.WriteFinal()
	assert.Equal(t, ErrFrameTooBig, err)

	et = newExchangeTester(t)
	defer et.Close()
	assert.NoError(t, et.Exchange.WriteByte(0x01))
	assert.NoError(t, et.Exchange.Flush())
	assert.NoError(t, et.Exchange.WriteByte(0x02))
	assert.NoError(t, et.Exchange.Flush())
	et.SubmitFrame(nil)
	err = et.Exchange.Stop()
	assert.Equal(t, ErrTimeoutFlowControl, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_ProxyResponse_transparency(t *testing.T) {
	// Make a frame for testing with
	et := newExchangeTester(t)
	defer et.Close()
	rr := httptest.NewRecorder()
	rr.WriteHeader(201)
	_, err := rr.WriteString("Meh")
	assert.NoError(t, err)
	assert.NoError(t, et.Exchange.WriteResponse(rr.Result()))
	n, err := et.Exchange.ReadFrom(rr.Body)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), n)
	testingFrame := et.Exchange.fdw
	assert.NotNil(t, testingFrame)
	assert.NoError(t, et.Exchange.WriteFinal())

	// Test transparency
	et = newExchangeTester(t)
	defer et.Close()
	et.SubmitFrame(testingFrame)
	rr2 := httptest.NewRecorder()
	err = et.Exchange.ProxyResponse(rr2)
	assert.NoError(t, err)
	assert.Equal(t, rr.Result(), rr2.Result())
}

func Test_Exchange_ProxyResponse_read_eof(t *testing.T) {
	// Test read error
	et := newExchangeTester(t)
	defer et.Close()
	et.SubmitFrame(nil)
	rr2 := httptest.NewRecorder()
	err := et.Exchange.ProxyResponse(rr2)
	assert.Equal(t, io.EOF, err)
}

func Test_Exchange_ProxyResponse_frame_missing_head(t *testing.T) {
	// Test frame missing head
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0x01)
	fd.Header().SetBody()
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	rr2 := httptest.NewRecorder()
	err := et.Exchange.ProxyResponse(rr2)
	assert.Equal(t, ErrMissingFrameHead, err)
}

func Test_Exchange_ProxyResponse_wrong_record_type(t *testing.T) {
	// Test wrong record type
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteRecordType(RecordTypeUserFirst)
	fd.Header().SetHead()
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	rr2 := httptest.NewRecorder()
	err := et.Exchange.ProxyResponse(rr2)
	assert.Equal(t, ErrUnhandledRecordType, err)
}

func Test_Exchange_Close(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.Header().SetBody()
	fd.Header().SetFinal()
	et.SubmitFrame(fd)
	err := et.Exchange.Close()
	assert.NoError(t, err)
}

func Test_Exchange_Stop(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	assert.NoError(t, et.Exchange.Start(et))
	assert.NoError(t, et.Exchange.Stop())
	et.Exchange.Release()
	assert.True(t, et.released)

	et = newExchangeTester(t)
	defer et.Close()
	et.Exchange.sendWindow--
	err := et.Exchange.Stop()
	assert.Equal(t, ErrTimeoutFlowControl, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_Serve(t *testing.T) {
	et := newExchangeTester(t)
	go et.Exchange.ServeHTTP(et)
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	et.SubmitFrame(nil)
}

func Test_Exchange_flowcontrol_errors(t *testing.T) {
	// flow control timeout
	et := newExchangeTester(t)
	defer et.Close()
	err := et.Exchange.WriteRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, 0xf0000))))
	assert.Equal(t, ErrTimeoutFlowControl, err)
}
