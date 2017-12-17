package rap

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type exchangeTester struct {
	t              *testing.T
	wg             sync.WaitGroup
	released       bool
	ackClosed      bool
	doneClosed     bool
	writeClosed    bool
	readClosed     bool
	forceReadError error
	writeCh        chan FrameData
	readCh         chan FrameData
	lastWritten    FrameData
	Exchange       *Exchange
}

func newExchangeTester(t *testing.T) *exchangeTester {
	et := &exchangeTester{
		t:       t,
		writeCh: make(chan FrameData),
		readCh:  make(chan FrameData, MaxSendWindowSize),
	}
	et.Exchange = NewExchange(et, MaxExchangeID)
	et.wg.Add(1)

	go func() {
		for {
			fd := <-et.writeCh
			if fd == nil {
				break
			}
			et.lastWritten = fd
		}
	}()

	return et
}

func (et *exchangeTester) ExchangeWrite(exchangeID ExchangeID, fd FrameData) error {
	if !et.writeClosed {
		et.writeCh <- fd
		return nil
	}
	return io.EOF
}

func (et *exchangeTester) ExchangeRead(exchangeID ExchangeID) (FrameData, error) {
	return <-et.readCh, et.forceReadError
}

func (et *exchangeTester) CloseWrite() {
	if !et.writeClosed {
		et.writeClosed = true
		close(et.writeCh)
	}
}

func (et *exchangeTester) CloseRead() {
	if !et.readClosed {
		et.readClosed = true
		close(et.readCh)
	}
}

func (et *exchangeTester) CloseAck() {
	if !et.ackClosed {
		et.ackClosed = true
		close(et.Exchange.ackCh)
	}
}

func (et *exchangeTester) ExchangeRelease(e *Exchange) {
	et.released = true
	et.Close()
}

func (et *exchangeTester) ExchangeTimeout() time.Duration {
	return time.Millisecond * 100
}

func (et *exchangeTester) Close() {
	et.CloseRead()
	et.CloseAck()
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
	et.readCh <- fd
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

func (fw *failWriter) failError() error {
	if fw.failWithError == nil {
		fw.failWithError = errFailWriter
	}
	return fw.failWithError
}

func Test_Exchange_String(t *testing.T) {
	e := NewExchange(&exchangeTester{}, 0x1)
	assert.Equal(t, "[Exchange [ExchangeID 0001] sendW=8 started=false sentC=false recvC=false len(ackCh)=0]", e.String())
}

func Test_Exchange_StartAndRelease(t *testing.T) {
	// Simple case
	et := newExchangeTester(t)
	defer et.Close()
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	et.Exchange.Start(et)
	et.Exchange.Release()
	assert.True(t, et.released)

	// EOF before starting
	et = newExchangeTester(t)
	defer et.Close()
	et.CloseRead()
	err := et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Empty frame
	et = newExchangeTester(t)
	defer et.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Empty frame sequence
	et = newExchangeTester(t)
	defer et.Close()
	fd = NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	et.readCh <- fd
	fd = NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Missing frame head
	et = newExchangeTester(t)
	defer et.Close()
	fd = NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetFinal()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Equal(t, ErrMissingFrameHead, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Invalid URL in request record
	et = newExchangeTester(t)
	defer et.Close()
	fd = NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.WriteStringNull()  // method
	fd.WriteString(":a:") // illegal url
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Error(t, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Incomplete request frame
	et = newExchangeTester(t)
	defer et.Close()
	fd = NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	fd.Header().SetFinal()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	et.readCh <- fd
	assert.Panics(t, func() {
		et.Exchange.Start(et)
	})
	et.Exchange.Release()
	assert.True(t, et.released)

	// Illegal record type
	et = newExchangeTester(t)
	defer et.Close()
	fd = NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeUserFirst - 1)
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Equal(t, ErrUnhandledRecordType, err)
	et.Exchange.Release()
	assert.True(t, et.released)
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
	fd.Header().SetFinal()
	et.readCh <- fd
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
	fd.Header().SetFinal()
	et.readCh <- fd
	var buf bytes.Buffer
	n, err := et.Exchange.WriteTo(&buf)
	assert.Equal(t, int64(1), n)
	assert.NoError(t, err)

	et = newExchangeTester(t)
	defer et.Close()
	fd = NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0xc5)
	fd.Header().SetFinal()
	et.readCh <- fd
	n, err = et.Exchange.WriteTo(&failWriter{})
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
	assert.NoError(t, et.Exchange.CloseWrite())
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
	err := et.Exchange.CloseWrite()
	assert.NoError(t, err)
	err = et.Exchange.CloseWrite()
	assert.Equal(t, io.ErrClosedPipe, err)

	et = newExchangeTester(t)
	defer et.Close()
	et.Exchange.writeStart()
	et.Exchange.fdw.Write(make([]byte, FrameMaxSize+1))
	err = et.Exchange.CloseWrite()
	assert.Equal(t, ErrFrameTooBig, err)

	et = newExchangeTester(t)
	defer et.Close()
	assert.NoError(t, et.Exchange.WriteByte(0x01))
	assert.NoError(t, et.Exchange.Flush())
	assert.NoError(t, et.Exchange.WriteByte(0x02))
	assert.NoError(t, et.Exchange.Flush())
	et.CloseRead()
	err = et.Exchange.Stop()
	assert.Equal(t, ErrTimeoutFlowControl, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_ProxyResponse(t *testing.T) {
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
	assert.NoError(t, et.Exchange.CloseWrite())

	// Test read error
	et = newExchangeTester(t)
	defer et.Close()
	et.CloseRead()
	rr2 := httptest.NewRecorder()
	err = et.Exchange.ProxyResponse(rr2)
	assert.Equal(t, io.EOF, err)

	// Test read error
	et = newExchangeTester(t)
	et.forceReadError = io.ErrUnexpectedEOF
	defer et.Close()
	et.CloseRead()
	rr2 = httptest.NewRecorder()
	err = et.Exchange.ProxyResponse(rr2)
	assert.Equal(t, io.ErrUnexpectedEOF, err)

	// Test frame missing head
	et = newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.WriteByte(0x01)
	fd.Header().SetBody()
	fd.Header().SetFinal()
	et.readCh <- fd
	rr2 = httptest.NewRecorder()
	err = et.Exchange.ProxyResponse(rr2)
	assert.Equal(t, ErrMissingFrameHead, err)

	// Test wrong record type
	et = newExchangeTester(t)
	defer et.Close()
	fd = NewFrameDataID(MaxExchangeID)
	fd.WriteRecordType(RecordTypeUserFirst)
	fd.Header().SetHead()
	fd.Header().SetFinal()
	et.readCh <- fd
	rr2 = httptest.NewRecorder()
	err = et.Exchange.ProxyResponse(rr2)
	assert.Equal(t, ErrUnhandledRecordType, err)

	// Test transparency
	et = newExchangeTester(t)
	defer et.Close()
	et.readCh <- testingFrame
	rr2 = httptest.NewRecorder()
	err = et.Exchange.ProxyResponse(rr2)
	assert.NoError(t, err)
	assert.Equal(t, rr.Result(), rr2.Result())
}

func Test_Exchange_Close(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	fd := NewFrameDataID(MaxExchangeID)
	fd.Header().SetFinal()
	et.readCh <- fd
	err := et.Exchange.Close()
	assert.NoError(t, err)

	et = newExchangeTester(t)
	defer et.Close()
	et.CloseRead()
	err = et.Exchange.Close()
	assert.Equal(t, io.EOF, err)
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
	et.CloseRead()
}

func Test_Exchange_flowcontrol_errors(t *testing.T) {
	// flow control timeout
	et := newExchangeTester(t)
	defer et.Close()
	err := et.Exchange.WriteRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, 0xf0000))))
	assert.Equal(t, ErrTimeoutFlowControl, err)

	// ack channel closed
	et = newExchangeTester(t)
	defer et.Close()
	et.CloseAck()
	err = et.Exchange.WriteRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, 0xf0000))))
	assert.Equal(t, io.ErrClosedPipe, err)
}
