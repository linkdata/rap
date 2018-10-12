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

type connTester struct {
	t           *testing.T
	wg          sync.WaitGroup
	once        sync.Once
	releasedCh  chan struct{}
	ackFn       func(*Conn)
	finFn       func(*Conn)
	mux         ConnMuxer
	Conn        *Conn
	handler     http.Handler
	lastWritten FrameData
	sentFinal   int32
}

type connTesterWriter struct {
	ct          *connTester
	writeCh     chan FrameData
	lastWritten FrameData
	closeCh     chan struct{}
	writeClosed int32
	timeoutErr  error
}

func newConnTesterWriter(ct *connTester) *connTesterWriter {
	ctw := &connTesterWriter{
		ct:         ct,
		writeCh:    make(chan FrameData),
		closeCh:    make(chan struct{}),
		timeoutErr: errors.New("newConnTesterWriter timeout"),
	}

	go func() {
		timeout := time.NewTimer(time.Minute)
		defer timeout.Stop()
		for {
			var fd FrameData
			select {
			case fd = <-ctw.writeCh:
			case <-timeout.C:
				log.Printf("%+v", ctw.timeoutErr)
				assert.Fail(ctw.ct.t, "timeout")
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
						if ctw.ct.sendingFinal() {
							if ctw.ct.finFn != nil {
								ctw.ct.finFn(ctw.ct.Conn)
							} else {
								if needsAck {
									ctw.ct.SubmitFrame(ct.Conn.makeFinalFrame(true))
								}
							}
						}
					}
				} else {
					ctw.lastWritten = fd
					if ctw.ct.ackFn != nil {
						ctw.ct.ackFn(ctw.ct.Conn)
					} else {
						ctw.ct.Conn.ackCh <- struct{}{}
					}
				}
			}
		}
		close(ctw.closeCh)
	}()

	return ctw
}

func (ctw *connTesterWriter) isWriteClosed() bool {
	return atomic.LoadInt32(&ctw.writeClosed) != 0
}

func (ctw *connTesterWriter) closingWrite() bool {
	return atomic.CompareAndSwapInt32(&ctw.writeClosed, 0, 1)
}

func (ctw *connTesterWriter) CloseWrite() {
	if ctw.closingWrite() {
		close(ctw.writeCh)
	}
}

func (ctw *connTesterWriter) Close() {
	ctw.CloseWrite()
}

func (ctw *connTesterWriter) WaitForClose() {
	timer := time.NewTimer(time.Second * 10)
	defer timer.Stop()
	select {
	case <-ctw.closeCh:
	case <-timer.C:
		assert.Fail(ctw.ct.t, "connTesterWriter timeout waiting for close")
	}
}

func (ctw *connTesterWriter) ConnWrite(fd FrameData) error {
	if !ctw.ct.isWriteClosed() {
		t := time.NewTimer(time.Second)
		defer t.Stop()
		select {
		case ctw.writeCh <- fd:
		case <-ctw.closeCh:
			return serverClosedError{}
		case <-t.C:
			log.Print("conn: ", ctw.ct.Conn)
			log.Print("fd: ", fd)
			panic("timeout waiting for ConnWrite to send")
		}
		return nil
	}
	return io.ErrClosedPipe
}

func (ctw *connTesterWriter) ConnRelease(conn *Conn) {
	ctw.ct.ConnRelease(conn)
}

func (ctw *connTesterWriter) ConnAbortChannel() <-chan struct{} {
	return ctw.closeCh
}

func newConnTester(t *testing.T) *connTester {
	ct := &connTester{
		t:          t,
		releasedCh: make(chan struct{}),
	}
	ct.mux = newConnTesterWriter(ct)
	ct.Conn = NewConn(ct, MaxConnID)
	ct.Conn.OnRecycle(ct.ConnRelease)
	ct.wg.Add(1)
	return ct
}

func newConnTesterUsingClient(t *testing.T, c *Client) *connTester {
	conn, err := c.NewConnMayDial()
	assert.NoError(t, err)
	assert.NotNil(t, conn)
	assert.NotNil(t, conn.mux)
	ct := &connTester{
		t:    t,
		mux:  conn.mux,
		Conn: conn,
	}
	conn.OnRecycle(ct.ConnRelease)
	ct.wg.Add(1)
	return ct
}

func (ct *connTester) isWriteClosed() bool {
	if ctw, ok := ct.mux.(*connTesterWriter); ok {
		return ctw.isWriteClosed()
	}
	return false
}

func (ct *connTester) closeWrite() bool {
	if ctw, ok := ct.mux.(*connTesterWriter); ok {
		return ctw.closingWrite()
	}
	return false
}

func (ct *connTester) Released() bool {
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-ct.releasedCh:
		return true
	case <-t.C:
		return false
	}
}

func (ct *connTester) CloseWrite() {
	if etw, ok := ct.mux.(*connTesterWriter); ok {
		etw.CloseWrite()
	}
}

func (ct *connTester) SubmitFrame(fd FrameData) error {
	ct.Conn.starting()
	return ct.Conn.SubmitFrame(fd)
}

func (ct *connTester) sendingFinal() bool {
	return atomic.CompareAndSwapInt32(&ct.sentFinal, 0, 1)
}

func (ct *connTester) SendFinal() {
	if ct.sendingFinal() {
		ct.SubmitFrame(ct.Conn.makeFinalFrame(false))
	}
}

func (ct *connTester) ConnWrite(fd FrameData) error {
	if !ct.isWriteClosed() {
		if fd != nil && fd.Header().HasPayload() {
			ct.lastWritten = fd
		}
		return ct.mux.ConnWrite(fd)
	}
	return io.ErrClosedPipe
}

func (ct *connTester) ConnRelease(conn *Conn) {
	ct.once.Do(func() { close(ct.releasedCh) })
}

func (ct *connTester) ConnAbortChannel() <-chan struct{} {
	return ct.mux.ConnAbortChannel()
}

func (ct *connTester) Close() {
	ct.Conn.Close()
	if ctw, ok := ct.mux.(*connTesterWriter); ok {
		ctw.CloseWrite()
		ctw.WaitForClose()
	}
}

func (ct *connTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

func (ct *connTester) InjectRequest(req *http.Request) {
	fd := NewFrameData()
	fd.WriteHeader(MaxConnID)
	err := fd.WriteRequest(req)
	assert.NoError(ct.t, err)
	var buf bytes.Buffer
	n, err := io.Copy(&buf, req.Body)
	if err == nil && n > 0 {
		fd.Header().SetBody()
		fd.Header().SetSizeValue(int(n))
		precopylen := len(fd)
		n2, err2 := io.Copy(&fd, &buf)
		assert.Equal(ct.t, precopylen+int(n2), len(fd))
		assert.NoError(ct.t, err2)
		assert.Equal(ct.t, n, n2)
	}
	ct.SubmitFrame(fd)
	ct.SendFinal()
}

type failWriterError struct {
	msg string // description of error
}

func (fwe *failWriterError) Error() string { return fwe.msg }

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

func Test_Conn_String(t *testing.T) {
	conn := NewConn(&connTester{}, 0x1)
	conn.setRunState(runStateWaitFin)
	conn.hijacking()
	conn.localSendingFinal()
	conn.remoteSendingFinal()
	expected := fmt.Sprintf("[Conn %v %v   FIN   HJ LF RF (%d+%d)]", conn.Serial(), conn.ID, SendWindowSize, 0)
	assert.Equal(t, expected, conn.String())
}

func Test_Conn_Release(t *testing.T) {
	// Simple case
	ct := newConnTester(t)
	defer ct.Close()
	ct.InjectRequest(httptest.NewRequest("GET", "/", nil))
	ct.Conn.Serve(ct)
	assert.True(t, ct.Released())
}

func Test_Conn_StartAndRelease_eof_before_starting(t *testing.T) {
	// EOF before starting
	ct := newConnTester(t)
	defer ct.Close()
	ct.SendFinal()
	err := ct.Conn.Serve(ct)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.True(t, ct.Released())
}

func Test_Conn_StartAndRelease_empty_frame(t *testing.T) {
	// Empty frame
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxConnID)
	fd.Header().SetHead()
	ct.SubmitFrame(fd)
	ct.SendFinal()
	err := ct.Conn.Serve(ct)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.True(t, ct.Released())
}

func Test_Conn_StartAndRelease_two_empty_frames(t *testing.T) {
	// Empty frame sequence
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameDataID(MaxConnID)
	fd.Header().SetHead()
	ct.SubmitFrame(fd)
	fd = NewFrameDataID(MaxConnID)
	fd.Header().SetHead()
	ct.SubmitFrame(fd)
	ct.SendFinal()
	err := ct.Conn.Serve(ct)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.True(t, ct.Released())
}

func Test_Conn_StartAndRelease_missing_frame_head(t *testing.T) {
	// Missing frame head
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxConnID)
	fd.Header().SetBody()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	ct.SubmitFrame(fd)
	assert.Panics(t, func() { ct.Conn.Serve(ct) })
	assert.True(t, ct.Released())
}

func Test_Conn_StartAndRelease_invalid_url_in_request_record(t *testing.T) {
	// Invalid URL in request record
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxConnID)
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.WriteStringNull() // method
	fd.WriteStringNull() // scheme
	fd.WriteRoute(":a:") // illegal url
	ct.SubmitFrame(fd)
	err := ct.Conn.Serve(ct)
	assert.Error(t, err)
	assert.True(t, ct.Released())
}

func Test_Conn_StartAndRelease_incomplete_request_frame(t *testing.T) {
	// Incomplete request frame
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxConnID)
	fd.WriteRecordType(RecordTypeHTTPRequest)
	ct.SubmitFrame(fd)
	assert.Panics(t, func() {
		ct.Conn.Serve(ct)
	})
	assert.True(t, ct.Released())
}

func Test_Conn_StartAndRelease_unhandled_record_type(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameData()
	fd.WriteHeader(MaxConnID)
	fd.WriteRecordType(RecordTypeUserFirst - 1)
	ct.SubmitFrame(fd)
	err := ct.Conn.Serve(ct)
	assert.Equal(t, ErrUnhandledRecordType{}, errors.Cause(err))
	assert.True(t, ct.Released())
}

func Test_Conn_StartAndRelease_missing_body_bit_panics(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	ct.Conn.writeStart()
	ct.Conn.fdw.Write(make([]byte, 1))
	var err error
	assert.Panics(t, func() { err = ct.Conn.Flush() })
	assert.NoError(t, err)
	err = ct.Conn.Close()
	assert.NoError(t, err)
	assert.True(t, ct.Released())
}

func Test_Conn_WriteByte(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	assert.Equal(t, ErrUnhandledRecordType{}, errors.Cause(ct.Conn.WriteUserRecordType(0x1)))

	assert.NoError(t, ct.Conn.WriteUserRecordType(0xFF))

	// HasBody is true after writing
	assert.NoError(t, ct.Conn.writeByte(0x01))
	assert.True(t, ct.Conn.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+2, ct.Conn.Buffered())

	// Fill up to limit
	ct.Conn.write(make([]byte, ct.Conn.Available()))
	assert.Equal(t, FrameMaxSize, ct.Conn.Buffered())
	assert.True(t, ct.Conn.fdw.Header().HasBody())

	// Write one more should flush and start a new body
	assert.NoError(t, ct.Conn.writeByte(0x01))
	assert.Equal(t, FrameHeaderSize+1, ct.Conn.Buffered())
	assert.True(t, ct.Conn.fdw.Header().HasBody())

	// write final frame
	ct.finFn = func(conn *Conn) {}
	ct.Conn.Close()

	// Flushing should fail since final is sent
	ct.Conn.write(make([]byte, ct.Conn.Available()))
	err := ct.Conn.WriteByte(0x01)
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(err))
	assert.True(t, ct.Conn.remoteSendingFinal())
	ct.Conn.recycle()
}

func Test_Conn_Write(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	// Empty after writeStart()
	ct.Conn.writeStart()
	assert.False(t, ct.Conn.fdw.Header().HasBody())
	assert.Equal(t, FrameMaxPayloadSize, ct.Conn.Available())

	assert.NoError(t, ct.Conn.WriteUserRecordType(0xFF))

	// HasBody is true after writing
	ct.Conn.writeByte(0x01)
	assert.True(t, ct.Conn.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+2, ct.Conn.Buffered())

	// Fill up to just under limit
	ct.Conn.write(make([]byte, ct.Conn.Available()-1))
	assert.Equal(t, FrameMaxSize-1, ct.Conn.Buffered())
	assert.True(t, ct.Conn.fdw.Header().HasBody())

	// Write one more byte than can be fit
	ct.Conn.write([]byte{0x04, 0x05})
	assert.True(t, ct.Conn.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, ct.Conn.Buffered())
}

func Test_Conn_Flush(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	assert.NoError(t, ct.Conn.WriteUserRecordType(0xFF))

	// Normal flush
	n, err := ct.Conn.Write([]byte{0x01, 0x02})
	assert.NoError(t, err)
	assert.Equal(t, 2, n)
	err = ct.Conn.Flush()
	assert.NoError(t, err)

	// Flush after Flush is a no-op
	err = ct.Conn.Flush()
	assert.NoError(t, err)

	// Overflow the frame and flush should error
	assert.NoError(t, ct.Conn.WriteUserRecordType(0xFF))
	ct.Conn.fdw = append(ct.Conn.fdw, make([]byte, FrameMaxPayloadSize+1)...)
	ct.Conn.fdw.Header().SetBody()
	assert.Equal(t, FrameMaxSize+2, len(ct.Conn.fdw))
	err = ct.Conn.Flush()
	assert.Equal(t, ErrFrameTooBig{}, errors.Cause(err))
}

func Test_Conn_Read(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameDataID(MaxConnID)
	fd.WriteByte(0xc4)
	fd.Header().SetBody()
	ct.SubmitFrame(fd)
	ct.SendFinal()
	// Read the one-byte body
	p1 := make([]byte, 1)
	n, err := ct.Conn.Read(p1)
	assert.NoError(t, err)
	assert.Equal(t, len(p1), n)
	assert.Equal(t, byte(0xc4), p1[0])
	// Read again, expecting EOF
	n, err = ct.Conn.Read(p1)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.Zero(t, n)
}

func Test_Conn_ReadFrom(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	assert.NoError(t, ct.Conn.WriteUserRecordType(0xFF))

	// Reading from nil
	n, err := ct.Conn.ReadFrom(nil)
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.Zero(t, n)

	// Read one byte
	var buf bytes.Buffer
	buf.WriteByte(0xc4)
	n, err = ct.Conn.ReadFrom(&buf)
	assert.Equal(t, int64(1), n)
	assert.NoError(t, err)

	// Read more than max frame size bytes
	m, err := buf.Write(make([]byte, FrameMaxSize+1))
	assert.NoError(t, err)
	assert.Equal(t, FrameMaxSize+1, m)
	n, err = ct.Conn.ReadFrom(&buf)
	assert.Equal(t, int64(FrameMaxSize+1), n)
	assert.NoError(t, err)

	assert.NoError(t, ct.Conn.Flush())
	assert.Zero(t, len(ct.Conn.fdw))
	assert.False(t, ct.Conn.hasLocalSentFinal())

	m, err = buf.Write(make([]byte, FrameMaxSize*2+1))
	assert.NotZero(t, m)
	assert.NoError(t, err)
	ct.Conn.Close()
	n, err = ct.Conn.ReadFrom(&buf)
	assert.Error(t, io.ErrClosedPipe, errors.Cause(err))
}

func Test_Conn_WriteTo(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameDataID(MaxConnID)
	fd.WriteByte(0xc4)
	fd.Header().SetBody()
	ct.SubmitFrame(fd)
	ct.SendFinal()
	var buf bytes.Buffer
	n, err := ct.Conn.WriteTo(&buf)
	assert.Equal(t, int64(1), n)
	assert.NoError(t, err)
}

func Test_Conn_WriteTo_FailWriter(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameDataID(MaxConnID)
	fd.WriteByte(0xc5)
	fd.Header().SetBody()
	ct.SubmitFrame(fd)
	n, err := ct.Conn.WriteTo(&failWriter{})
	assert.Equal(t, int64(0), n)
	assert.Error(t, err)
}

func Test_Conn_WriteRequest(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	err := ct.Conn.WriteRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer([]byte{0xde, 0xad})))
	assert.False(t, ct.Conn.hasLocalSentFinal())
	assert.NoError(t, err)

	ct = newConnTester(t)
	defer ct.Close()
	ct.finFn = func(conn *Conn) {} // ignore the final frame from Close()
	ct.Conn.starting()
	assert.NoError(t, ct.Conn.Close())
	assert.True(t, ct.Conn.hasLocalSentFinal())
	assert.False(t, ct.Conn.hasRemoteSentFinal())
	err = ct.Conn.WriteRequest(httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(err))
}

func Test_Conn_WriteResponse(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	rr := httptest.NewRecorder()
	rr.WriteString("Meh")
	rr.WriteHeader(200)
	err := ct.Conn.WriteResponse(rr.Result())
	assert.False(t, ct.Conn.hasLocalSentFinal())
	assert.NoError(t, err)
}

func Test_Conn_CloseWrite(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	ct.Conn.starting()
	close(ct.Conn.localClosed)
	err := ct.Conn.Close()
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(err))

	ct = newConnTester(t)
	defer ct.Close()
	ct.Conn.writeStart()
	ct.Conn.fdw.Write(make([]byte, FrameMaxSize+1))
	ct.Conn.fdw.Header().SetBody()
	err = ct.Conn.Flush()
	assert.Equal(t, ErrFrameTooBig{}, errors.Cause(err))
	err = ct.Conn.Close()
	assert.NoError(t, err)

	ct = newConnTester(t)
	defer ct.Close()
	assert.NoError(t, ct.Conn.WriteUserRecordType(0xFF))
	assert.NoError(t, ct.Conn.WriteByte(0x01))
	assert.NoError(t, ct.Conn.Flush())
	assert.NoError(t, ct.Conn.WriteByte(0x02))
	assert.NoError(t, ct.Conn.Flush())
	ct.SendFinal()
	err = ct.Conn.loadFrameReader()
	assert.Equal(t, io.EOF, errors.Cause(err))
	assert.Zero(t, len(ct.Conn.readCh))
	assert.NoError(t, ct.Conn.Close())
	assert.True(t, ct.Released())
}

func Test_Conn_ProxyResponse_transparency(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	// Make a frame for testing with
	ct := newConnTester(t)
	defer ct.Close()
	rr := httptest.NewRecorder()
	rr.WriteHeader(201)
	_, err := rr.WriteString("Meh")
	assert.NoError(t, err)
	assert.NoError(t, ct.Conn.WriteResponse(rr.Result()))
	assert.NoError(t, ct.Conn.Close())
	assert.True(t, ct.Released())

	testingFrame := ct.lastWritten
	assert.NotNil(t, testingFrame)
	assert.True(t, testingFrame.Header().HasPayload())
	assert.Equal(t, 9, testingFrame.Header().PayloadSize())
	assert.Equal(t, 201, rr.Code)
	assert.Equal(t, int(3), rr.Body.Len())

	assert.NoError(t, ct.Conn.Close())

	// Test transparency
	ct2 := newConnTester(t)
	defer ct2.Close()
	ct2.SubmitFrame(testingFrame)
	ct2.SendFinal()
	rr2 := httptest.NewRecorder()
	_, err = ct2.Conn.ProxyResponse(rr2)
	assert.NoError(t, err)
	_, err = ct2.Conn.WriteTo(rr2)
	assert.Error(t, io.EOF, errors.Cause(err))
	assert.True(t, ct2.Conn.hasRemoteSentFinal())
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

func Test_Conn_ProxyResponse_read_eof(t *testing.T) {
	// Test read error
	ct := newConnTester(t)
	defer ct.Close()
	ct.SendFinal()
	rr2 := httptest.NewRecorder()
	_, err := ct.Conn.ProxyResponse(rr2)
	assert.True(t, ct.Conn.hasRemoteSentFinal())
	assert.Equal(t, io.EOF, errors.Cause(err))
}

func Test_Conn_ProxyResponse_wrong_record_type(t *testing.T) {
	// Test wrong record type
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameDataID(MaxConnID)
	fd.WriteRecordType(RecordTypeUserFirst)
	ct.SubmitFrame(fd)
	ct.SendFinal()
	rr2 := httptest.NewRecorder()
	_, err := ct.Conn.ProxyResponse(rr2)
	assert.True(t, ct.Conn.hasRemoteSentFinal())
	assert.Equal(t, ErrUnhandledRecordType{}.Error(), errors.Cause(err).Error())
}

func Test_Conn_Close(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	fd := NewFrameDataID(MaxConnID)
	fd.Header().SetBody()
	ct.SubmitFrame(fd)
	err := ct.Conn.Close()
	assert.NoError(t, err)
}

func Test_Conn_Stop(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	ct.InjectRequest(httptest.NewRequest("GET", "/", nil))
	assert.NoError(t, ct.Conn.Serve(ct))
	assert.NoError(t, ct.Conn.Close())
	assert.True(t, ct.Released())
}

func Test_Conn_Serve(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		ct.Conn.Serve(ct)
		wg.Done()
	}()
	ct.InjectRequest(httptest.NewRequest("GET", "/", nil))
	ct.SendFinal()
	wg.Wait()
}

func Test_Conn_flowcontrol_errors(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	ct := newConnTester(t)
	defer ct.Close()
	ct.ackFn = func(conn *Conn) {
		if conn.getSendWindow() == 0 {
			conn.SetWriteDeadline(time.Now().Add(time.Millisecond * 10))
		}
	}
	err := ct.Conn.WriteRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, FrameMaxPayloadSize*(MaxSendWindowSize+1)))))
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
	assert.Zero(t, len(ct.Conn.ackCh))
	assert.Zero(t, ct.Conn.getSendWindow())
	ct.Conn.sendWindow = int32(SendWindowSize)
}

func Test_Conn_SetDeadline_on_closed(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	ct.finFn = func(conn *Conn) {}
	ct.Conn.starting()
	ct.Conn.Close()
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(ct.Conn.SetDeadline(time.Now())))
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(ct.Conn.SetReadDeadline(time.Now())))
	assert.Equal(t, io.ErrClosedPipe, errors.Cause(ct.Conn.SetWriteDeadline(time.Now())))
	assert.True(t, ct.Conn.remoteSendingFinal())
	ct.Conn.recycle()
}

func makeNetConnPipe() (e1, e2 net.Conn, stop func(), err error) {
	return makeRapConnPipe()
}

func makeRapConnPipe() (c1, c2 *Conn, stop func(), err error) {
	p1, p2 := net.Pipe()

	wg := sync.WaitGroup{}

	m1 := NewMuxer(p1)
	wg.Add(2)
	go func() { defer wg.Done(); m1.WriteTo(p1) }()
	go func() { defer wg.Done(); m1.ReadFrom(p1) }()

	m2 := NewMuxer(p2)
	wg.Add(2)
	go func() { defer wg.Done(); m2.WriteTo(p2) }()
	go func() { defer wg.Done(); m2.ReadFrom(p2) }()

	c1 = m1.NewConn()
	c2 = m2.connLookup[c1.ID]

	return c1, c2, func() {
		c1.Close()
		c2.Close()
		m2.Close()
		m1.Close()
		p1.Close()
		p2.Close()
		wg.Wait()
	}, nil
}

func Test_Conn_makeNetConnPipe(t *testing.T) {
	c1, c2, stop, err := makeNetConnPipe()
	assert.NoError(t, err)
	assert.NotNil(t, c1)
	assert.NotNil(t, c2)
	assert.NotNil(t, stop)
	assert.NotPanics(t, stop)
}

func Test_Conn_flowcontrol_halts(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	c1, c2, stop, err := makeRapConnPipe()
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

func Test_Conn_transparency(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	testNetConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		c1, c2 = net.Pipe()
		stop = func() {
			c1.Close()
			c2.Close()
		}
		return
	})
	testNetConn(t, makeNetConnPipe)
}

func Test_Conn_Addr_interface(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	la := ct.Conn.LocalAddr()
	assert.NotNil(t, la)
	assert.Equal(t, "rap", la.Network())
	assert.Equal(t, "rap", la.String())

	ra := ct.Conn.RemoteAddr()
	assert.NotNil(t, ra)
	assert.Equal(t, "rap", ra.Network())
	assert.Equal(t, "rap", ra.String())
}

func Test_Conn_final_frame_with_body_panics(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	fd := ct.Conn.makeFinalFrame(false)
	fd.WriteByte(0)
	fd.SetSizeValue()

	assert.Panics(t, func() {
		ct.Conn.SubmitFrame(fd)
	})
}

func Test_Conn_multiple_final_frames_panics(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	fd := ct.Conn.makeFinalFrame(false)
	err := ct.SubmitFrame(fd)
	assert.NoError(t, err)

	fd = ct.Conn.makeFinalFrame(false)
	assert.Panics(t, func() {
		ct.SubmitFrame(fd)
	})
}

func Test_Conn_recycle_with_only_local_closed_panics(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	ct.Conn.starting()
	close(ct.Conn.localClosed)

	assert.Panics(t, func() {
		ct.Conn.recycle()
	})

	assert.True(t, ct.Conn.remoteSendingFinal())

}

func Test_Conn_manually_sending_final_frame_panics(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()

	ct.Conn.writeByte(0)
	ct.Conn.fdw.Header().SetFlow()

	assert.Panics(t, func() {
		ct.Conn.Flush()
	})
}
