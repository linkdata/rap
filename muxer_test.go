package rap

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "net/http/pprof"

	"github.com/fortytw2/leaktest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

type rwcPipe struct {
	io.ReadCloser
	io.WriteCloser
	bytesWritten int64
	bytesRead    int64
}

func (rwcp *rwcPipe) Close() error {
	if err := rwcp.WriteCloser.Close(); err != nil {
		return err
	}
	return rwcp.ReadCloser.Close()
}

func (rwcp *rwcPipe) AddBytesWritten(n int64) {
	atomic.AddInt64(&rwcp.bytesWritten, n)
}

func (rwcp *rwcPipe) AddBytesRead(n int64) {
	atomic.AddInt64(&rwcp.bytesRead, n)
}

func newRwcPipes() (a, b *rwcPipe) {
	ra, wa := io.Pipe()
	rb, wb := io.Pipe()
	a = &rwcPipe{
		ReadCloser:  rb,
		WriteCloser: wa,
	}
	b = &rwcPipe{
		ReadCloser:  ra,
		WriteCloser: wb,
	}
	return
}

type muxerTester struct {
	t                      *testing.T
	a, b                   *rwcPipe
	muxClient              *Muxer
	muxServer              *Muxer
	isClosed               int32
	injectFramesAtClose    bool
	expectServerError      error
	expectClientError      error
	expectClientCloseError error
	expectServerCloseError error
	serverDone             chan struct{}
	clientDone             chan struct{}
}

func (mt *muxerTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
	if req.Body != nil {
		if req.ContentLength > 0 {
			io.CopyN(ioutil.Discard, req.Body, req.ContentLength)
		} else if req.ContentLength == -1 {
			io.Copy(ioutil.Discard, req.Body)
		}
	}
}

func (mt *muxerTester) Start() {
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func(ct *muxerTester) {
		wg.Done()
		err := ct.muxServer.ServeHTTP(ct)
		if ct.expectServerError != nil {
			assert.Equal(ct.t, ct.expectServerError.Error(), errors.Cause(err).Error())
			assert.NotNil(ct.t, err)
		} else {
			if atomic.LoadInt32(&ct.isClosed) == 0 {
				log.Println("ct.server.ServeHTTP(ct) premature exit")
				assert.NoError(ct.t, err)
			}
			switch errors.Cause(err) {
			case nil:
			case io.EOF:
			case serverClosedError{}:
			case io.ErrClosedPipe:
			default:
				assert.NoError(ct.t, err)
			}
		}
		close(ct.serverDone)
	}(mt)
	wg.Wait()

	wg.Add(1)
	go func(ct *muxerTester) {
		wg.Done()
		err := ct.muxClient.ServeHTTP(nil)
		if ct.expectClientError != nil {
			assert.NotNil(ct.t, err)
			assert.Equal(ct.t, ct.expectClientError.Error(), errors.Cause(err).Error())
		} else {
			if atomic.LoadInt32(&ct.isClosed) == 0 {
				log.Println("ct.muxClient.ServeHTTP(ct) premature exit")
				assert.NoError(ct.t, err)
			}
			if err != nil && !isClosedError(err) {
				assert.NoError(ct.t, err)
			}
		}
		close(ct.clientDone)
	}(mt)
	wg.Wait()
}

func (mt *muxerTester) Close() {
	if atomic.CompareAndSwapInt32(&mt.isClosed, 0, 1) {
		for i := 0; i < 100; i++ {
			time.Sleep(10 * time.Millisecond)
			if len(mt.muxClient.writeCh) == 0 && len(mt.muxServer.writeCh) == 0 {
				break
			}
		}
		if mt.injectFramesAtClose {
			mt.muxClient.ConnWrite(nil)
			mt.muxClient.ConnWrite(nil)
		}
		err := mt.a.WriteCloser.Close()
		if mt.expectClientCloseError != nil {
			assert.Equal(mt.t, mt.expectClientCloseError.Error(), errors.Cause(err).Error())
			assert.Error(mt.t, err)
		} else {
			assert.NoError(mt.t, err)
		}
		err = mt.b.WriteCloser.Close()
		if mt.expectServerCloseError != nil {
			assert.Equal(mt.t, mt.expectServerCloseError.Error(), errors.Cause(err).Error())
			assert.Error(mt.t, err)
		} else {
			assert.NoError(mt.t, err)
		}
		<-mt.serverDone
		<-mt.clientDone
	}
}

func newMuxerTesterNotStarted(t *testing.T) (mt *muxerTester) {
	a, b := newRwcPipes()
	mt = &muxerTester{
		t:          t,
		a:          a,
		b:          b,
		muxClient:  NewMuxer(a),
		muxServer:  NewMuxer(b),
		serverDone: make(chan struct{}),
		clientDone: make(chan struct{}),
	}
	mt.muxClient.StatsCollector = a
	mt.muxServer.StatsCollector = b
	return
}

func newMuxerTester(t *testing.T) (mt *muxerTester) {
	mt = newMuxerTesterNotStarted(t)
	mt.Start()
	return
}

func (mt *muxerTester) InjectRequestNoErrors(r *http.Request) {
	requestErr, responseErr := mt.InjectRequest(r)
	if errors.Cause(responseErr) == io.EOF {
		responseErr = nil
	}
	assert.NoError(mt.t, requestErr)
	assert.NoError(mt.t, responseErr)
}

func (mt *muxerTester) InjectRequest(r *http.Request) (requestErr, responseErr error) {
	w := httptest.NewRecorder()
	e := mt.muxClient.NewConnWait(time.Second * 10)
	defer e.Close()
	requestErr = e.WriteRequest(r)
	_, responseErr = e.ProxyResponse(w)
	return
}

func (mt *muxerTester) FillConns() (gotten int) {
	for {
		if e := mt.muxClient.NewConn(); e != nil {
			gotten++
			e.OnRecycle(mt.muxClient.ConnRelease)
			defer e.Close()
		} else {
			return
		}
	}
}

func Test_Muxer_String(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}
	mt := newMuxerTester(t)
	defer mt.Close()
	expected := fmt.Sprintf("[Muxer %x]", mt.muxClient.serialNumber)
	assert.Equal(t, expected, mt.muxClient.String())
	assert.Equal(t, int(MaxConnID)+1, len(mt.muxClient.connLookup))
	assert.Equal(t, int(MaxConnID), cap(mt.muxClient.conns))
}

func Test_Muxer_conns_exhausted(t *testing.T) {
	mt := newMuxerTester(t)
	defer mt.Close()

	var firstConn *Conn

	for {
		if conn := mt.muxClient.NewConn(); conn != nil {
			if firstConn == nil {
				firstConn = conn
			} else {
				defer conn.Close()
			}
		} else {
			break
		}
	}
	assert.Nil(t, mt.muxClient.NewConnWait(time.Millisecond*10))
	firstConn.Close()
	conn2 := mt.muxClient.NewConnWait(time.Second * 10)
	assert.NotNil(t, conn2)
	conn2.Close()
}

func Test_Muxer_ReleaseConn(t *testing.T) {
	mt := newMuxerTester(t)
	defer mt.Close()
	e := mt.muxClient.NewConn()
	assert.NotNil(t, e)
	e.Close()
}

func Test_Muxer_conn_overflow(t *testing.T) {
	mt := newMuxerTester(t)
	defer mt.Close()
	gotten := mt.FillConns()
	assert.Equal(t, int(MaxConnID), gotten)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for len(mt.muxClient.conns) != int(MaxConnID) {
		select {
		case <-timer.C:
			assert.Equal(t, int(MaxConnID), len(mt.muxClient.conns))
		default:
		}
	}
	assert.Equal(t, int(MaxConnID), len(mt.muxClient.conns))
	conn := NewConn(mt.muxClient, 1)
	conn.OnRecycle(mt.muxClient.ConnRelease)
	assert.True(t, conn.remoteSendingFinal())
	assert.Panics(t, func() { conn.Close() })
}

func Test_Muxer_empty_request_response(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}
	mt := newMuxerTester(t)
	defer mt.Close()
	mt.InjectRequestNoErrors(httptest.NewRequest("GET", "/", nil))
}

func Test_Muxer_big_request_response(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}
	mt := newMuxerTester(t)
	defer mt.Close()
	mt.InjectRequestNoErrors(httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, 0xf0000))))
}

func Test_Muxer_muxercontrol_ping_pong(t *testing.T) {
	mt := newMuxerTester(t)
	defer mt.Close()
	assert.Zero(t, mt.muxClient.lastPingSent)
	assert.Zero(t, mt.muxClient.lastPongRcvd)
	assert.Zero(t, mt.muxServer.lastPingSent)
	assert.Zero(t, mt.muxServer.lastPongRcvd)
	assert.Zero(t, mt.muxClient.Latency())
	mt.muxClient.Ping()
	for i := 0; i < 10 && atomic.LoadInt64(&mt.muxClient.lastPongRcvd) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	assert.NotZero(t, mt.muxClient.lastPingSent)
	assert.NotZero(t, mt.muxClient.lastPongRcvd)
	assert.Zero(t, mt.muxServer.lastPingSent)
	assert.Zero(t, mt.muxServer.lastPongRcvd)
	assert.True(t, mt.muxClient.lastPingSent <= mt.muxClient.lastPongRcvd)
	if mt.muxClient.lastPingSent < mt.muxClient.lastPongRcvd {
		assert.NotZero(t, mt.muxClient.Latency())
	} else {
		assert.Zero(t, mt.muxClient.Latency())
	}
}

func Test_Muxer_muxercontrol_pinghandler_closed_before_pong(t *testing.T) {
	mt := newMuxerTester(t)
	defer mt.Close()
	mt.expectServerError = serverClosedError{}
	fd := FrameDataAlloc()
	fd.WriteMuxerControl(MuxerControlPing)
	fd.WriteInt64(time.Now().UnixNano())
	fd.SetSizeValue()
	close(mt.muxServer.doneChan)
	err := muxerControlPingHandler(mt.muxServer, fd)
	assert.Equal(t, serverClosedError{}, errors.Cause(err))
}

func Test_Muxer_muxercontrol_reserved(t *testing.T) {
	mt := newMuxerTesterNotStarted(t)
	defer mt.Close()
	mt.expectServerError = ProtocolError{}
	mt.expectClientError = io.EOF
	mt.Start()
	fd := FrameDataAlloc()
	fd.WriteMuxerControl(muxerControlReserved001)
	mt.muxClient.ConnWrite(fd)
	<-mt.serverDone
}

func Test_Muxer_muxercontrol_panic(t *testing.T) {
	mt := newMuxerTesterNotStarted(t)
	defer mt.Close()
	mt.expectClientError = io.EOF
	mt.expectServerError = PanicError{}
	mt.Start()
	fd := FrameDataAlloc()
	fd.WriteMuxerControl(MuxerControlPanic)
	fd.WriteString("Some text")
	fd.SetSizeValue()
	mt.muxClient.ConnWrite(fd)
	<-mt.serverDone
}

func Test_Muxer_ServeHTTP_write_error(t *testing.T) {
	mt := newMuxerTesterNotStarted(t)
	defer mt.Close()
	mt.a.WriteCloser = &failWriter{
		failAtCount: 1,
		failOnWrite: true,
		WriteCloser: mt.a.WriteCloser,
	}
	mt.expectClientError = errFailWriter
	mt.expectServerError = io.ErrUnexpectedEOF
	mt.Start()
	mt.muxClient.Ping()
	<-mt.serverDone
}

func Test_Muxer_ServeHTTP_write_close_error(t *testing.T) {
	mt := newMuxerTesterNotStarted(t)
	mt.a.WriteCloser = &failWriter{
		failOnClose: true,
		WriteCloser: mt.a.WriteCloser,
	}
	mt.expectClientCloseError = errFailWriter
	mt.expectClientError = errFailWriter
	mt.Start()
	mt.muxClient.Ping()
	mt.Close()
}
