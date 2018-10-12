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
	expectConnError        error
	expectConnCloseError   error
	expectServerCloseError error
	serverDone             chan struct{}
	connDone               chan struct{}
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
		if ct.expectConnError != nil {
			assert.NotNil(ct.t, err)
			assert.Equal(ct.t, ct.expectConnError.Error(), errors.Cause(err).Error())
		} else {
			if atomic.LoadInt32(&ct.isClosed) == 0 {
				log.Println("ct.conn.ServeHTTP(ct) premature exit")
				assert.NoError(ct.t, err)
			}
			if err != nil && !isClosedError(err) {
				assert.NoError(ct.t, err)
			}
		}
		close(ct.connDone)
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
			mt.muxClient.ExchangeWrite(nil)
			mt.muxClient.ExchangeWrite(nil)
		}
		err := mt.a.WriteCloser.Close()
		if mt.expectConnCloseError != nil {
			assert.Equal(mt.t, mt.expectConnCloseError.Error(), errors.Cause(err).Error())
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
		<-mt.connDone
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
		connDone:   make(chan struct{}),
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
	e := mt.muxClient.NewExchangeWait(time.Second * 10)
	defer e.Close()
	requestErr = e.WriteRequest(r)
	_, responseErr = e.ProxyResponse(w)
	return
}

func (mt *muxerTester) FillExchanges() (gotten int) {
	for {
		if e := mt.muxClient.NewExchange(); e != nil {
			gotten++
			e.OnRecycle(mt.muxClient.ExchangeRelease)
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
	expected := fmt.Sprintf("[Conn %x]", mt.muxClient.serialNumber)
	assert.Equal(t, expected, mt.muxClient.String())
	assert.Equal(t, int(MaxExchangeID)+1, len(mt.muxClient.exchangeLookup))
	assert.Equal(t, int(MaxExchangeID), cap(mt.muxClient.exchanges))
}

func Test_Muxer_exchanges_exhausted(t *testing.T) {
	mt := newMuxerTester(t)
	defer mt.Close()

	var firstExchange *Exchange

	for {
		if e := mt.muxClient.NewExchange(); e != nil {
			if firstExchange == nil {
				firstExchange = e
			} else {
				defer e.Close()
			}
		} else {
			break
		}
	}
	assert.Nil(t, mt.muxClient.NewExchangeWait(time.Millisecond*10))
	firstExchange.Close()
	e2 := mt.muxClient.NewExchangeWait(time.Second * 10)
	assert.NotNil(t, e2)
	e2.Close()
}

func Test_Muxer_ReleaseExchange(t *testing.T) {
	mt := newMuxerTester(t)
	defer mt.Close()
	e := mt.muxClient.NewExchange()
	assert.NotNil(t, e)
	e.Close()
}

func Test_Muxer_exchange_overflow(t *testing.T) {
	mt := newMuxerTester(t)
	defer mt.Close()
	gotten := mt.FillExchanges()
	assert.Equal(t, int(MaxExchangeID), gotten)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for len(mt.muxClient.exchanges) != int(MaxExchangeID) {
		select {
		case <-timer.C:
			assert.Equal(t, int(MaxExchangeID), len(mt.muxClient.exchanges))
		default:
		}
	}
	assert.Equal(t, int(MaxExchangeID), len(mt.muxClient.exchanges))
	e := NewExchange(mt.muxClient, 1)
	e.OnRecycle(mt.muxClient.ExchangeRelease)
	assert.True(t, e.remoteSendingFinal())
	assert.Panics(t, func() { e.Close() })
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

func Test_Muxer_conncontrol_ping_pong(t *testing.T) {
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

func Test_Muxer_conncontrol_pinghandler_closed_before_pong(t *testing.T) {
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

func Test_Muxer_conncontrol_reserved(t *testing.T) {
	mt := newMuxerTesterNotStarted(t)
	defer mt.Close()
	mt.expectServerError = ProtocolError{}
	mt.expectConnError = io.EOF
	mt.Start()
	fd := FrameDataAlloc()
	fd.WriteMuxerControl(muxerControlReserved001)
	mt.muxClient.ExchangeWrite(fd)
	<-mt.serverDone
}

func Test_Muxer_conncontrol_panic(t *testing.T) {
	mt := newMuxerTesterNotStarted(t)
	defer mt.Close()
	mt.expectConnError = io.EOF
	mt.expectServerError = PanicError{}
	mt.Start()
	fd := FrameDataAlloc()
	fd.WriteMuxerControl(MuxerControlPanic)
	fd.WriteString("Some text")
	fd.SetSizeValue()
	mt.muxClient.ExchangeWrite(fd)
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
	mt.expectConnError = errFailWriter
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
	mt.expectConnCloseError = errFailWriter
	mt.expectConnError = errFailWriter
	mt.Start()
	mt.muxClient.Ping()
	mt.Close()
}
