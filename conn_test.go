package rap

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/stretchr/testify/assert"
)

const connTimeout = time.Millisecond * time.Duration(10)

func init() {
	// limit goroutines to avoid debugger overload
	if int(MaxExchangeID) > 512 {
		MaxExchangeID = ExchangeID(512)
	}
}

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

type connTester struct {
	t                      *testing.T
	a, b                   *rwcPipe
	conn                   *Conn
	server                 *Conn
	isClosed               bool
	injectFramesAtClose    bool
	expectServerError      reflect.Type
	expectConnError        reflect.Type
	expectConnCloseError   reflect.Type
	expectServerCloseError reflect.Type
	serverDone             chan struct{}
	connDone               chan struct{}
}

func (ct *connTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
}

func (ct *connTester) Start() {
	go func(ct *connTester) {
		err := ct.server.ServeHTTP(ct)
		if ct.expectServerError != nil {
			assert.Equal(ct.t, ct.expectServerError, reflect.TypeOf(err))
			assert.NotNil(ct.t, err)
		} else {
			if !ct.isClosed {
				panic(fmt.Sprint("ct.server.ServeHTTP(ct) premature exit: ", err))
			}
			if err != nil && err != io.EOF {
				assert.NoError(ct.t, err)
			}
		}
		close(ct.serverDone)
	}(ct)
	go func(ct *connTester) {
		err := ct.conn.ServeHTTP(nil)
		if ct.expectConnError != nil {
			assert.Equal(ct.t, ct.expectConnError, reflect.TypeOf(err))
			assert.NotNil(ct.t, err)
		} else {
			if !ct.isClosed {
				panic(fmt.Sprint("ct.conn.ServeHTTP(ct) premature exit: ", err))
			}
			if err != nil && err != io.EOF {
				assert.NoError(ct.t, err)
			}
		}
		close(ct.connDone)
	}(ct)
}

func (ct *connTester) Close() {
	if !ct.isClosed {
		ct.isClosed = true
		for i := 0; i < 100; i++ {
			time.Sleep(10 * time.Millisecond)
			if len(ct.conn.writeCh) == 0 && len(ct.server.writeCh) == 0 {
				break
			}
		}
		if ct.injectFramesAtClose {
			ct.conn.writeCh <- nil
			ct.conn.writeCh <- nil
		}
		err := ct.a.WriteCloser.Close()
		if ct.expectConnCloseError != nil {
			assert.Equal(ct.t, ct.expectConnCloseError, reflect.TypeOf(err))
			assert.Error(ct.t, err)
		} else {
			assert.NoError(ct.t, err)
		}
		err = ct.b.WriteCloser.Close()
		if ct.expectServerCloseError != nil {
			assert.Equal(ct.t, ct.expectServerCloseError, reflect.TypeOf(err))
			assert.Error(ct.t, err)
		} else {
			assert.NoError(ct.t, err)
		}
		<-ct.serverDone
		<-ct.connDone
	}
}

func newConnTesterNotStarted(t *testing.T) (ct *connTester) {
	a, b := newRwcPipes()
	ct = &connTester{
		t:          t,
		a:          a,
		b:          b,
		conn:       NewConn(a),
		server:     NewConn(b),
		serverDone: make(chan struct{}),
		connDone:   make(chan struct{}),
	}
	ct.conn.StatsCollector = a
	ct.server.StatsCollector = b
	return
}

func newConnTester(t *testing.T) (ct *connTester) {
	ct = newConnTesterNotStarted(t)
	ct.Start()
	return
}

func (ct *connTester) InjectRequest(r *http.Request) {
	w := httptest.NewRecorder()
	var requestErr error
	var responseErr error
	var wg sync.WaitGroup
	e := ct.conn.NewExchangeWait(connTimeout)
	defer e.Release()
	wg.Add(1)
	go func() {
		defer wg.Done()
		requestErr = e.WriteRequest(r)
	}()
	responseErr = e.ProxyResponse(w)
	wg.Wait()
	assert.NoError(ct.t, requestErr)
	assert.NoError(ct.t, responseErr)
}

func Test_Conn_String(t *testing.T) {
	defer leaktest.Check(t)()
	ct := newConnTester(t)
	defer ct.Close()
	assert.Equal(t, "[Conn]", ct.conn.String())
	assert.Equal(t, int(MaxExchangeID)+1, len(ct.conn.exchangeLookup))
	assert.Equal(t, int(MaxExchangeID)+1, cap(ct.conn.exchanges))
}

func Test_Conn_NewExchange(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	var gotten int
	for {
		if e := ct.conn.NewExchange(); e != nil {
			gotten++
			defer e.Release()
		} else {
			break
		}
	}
	assert.Equal(t, int(MaxExchangeID)+1, gotten)
	assert.Nil(t, ct.conn.NewExchangeWait(time.Millisecond))
}

func Test_Conn_ReleaseExchange(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	e := ct.conn.NewExchange()
	assert.NotNil(t, e)
	e.Release()
	e = NewExchange(ct.conn, 1)
	assert.Panics(t, func() { e.Release() })
}

func Test_Conn_empty_request_response(t *testing.T) {
	defer leaktest.Check(t)()
	ct := newConnTester(t)
	defer ct.Close()
	ct.InjectRequest(httptest.NewRequest("GET", "/", nil))
}

func Test_Conn_big_request_response(t *testing.T) {
	defer leaktest.Check(t)()
	ct := newConnTester(t)
	defer ct.Close()
	ct.InjectRequest(httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, 0xf0000))))
}

func Test_Conn_conncontrol_ping_pong(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	assert.Zero(t, ct.conn.lastPingSent)
	assert.Zero(t, ct.conn.lastPongRcvd)
	assert.Zero(t, ct.server.lastPingSent)
	assert.Zero(t, ct.server.lastPongRcvd)
	assert.Zero(t, ct.conn.Latency())
	ct.conn.Ping()
	for i := 0; i < 10 && atomic.LoadInt64(&ct.conn.lastPongRcvd) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	assert.NotZero(t, ct.conn.lastPingSent)
	assert.NotZero(t, ct.conn.lastPongRcvd)
	assert.Zero(t, ct.server.lastPingSent)
	assert.Zero(t, ct.server.lastPongRcvd)
	assert.True(t, ct.conn.lastPingSent <= ct.conn.lastPongRcvd)
	if ct.conn.lastPingSent < ct.conn.lastPongRcvd {
		assert.NotZero(t, ct.conn.Latency())
	} else {
		assert.Zero(t, ct.conn.Latency())
	}
}

func Test_Conn_conncontrol_reserved(t *testing.T) {
	ct := newConnTesterNotStarted(t)
	defer ct.Close()
	ct.expectServerError = reflect.TypeOf((*ProtocolError)(nil))
	ct.Start()
	fd := FrameDataAlloc()
	fd.WriteConnControl(connControlReserved000)
	ct.conn.writeCh <- fd
}

func Test_Conn_conncontrol_panic(t *testing.T) {
	ct := newConnTesterNotStarted(t)
	defer ct.Close()
	ct.expectConnError = reflect.TypeOf(io.EOF)
	ct.expectServerError = reflect.TypeOf((*PanicError)(nil))
	ct.Start()
	fd := FrameDataAlloc()
	fd.WriteConnControl(ConnControlPanic)
	fd.WriteString("Some text")
	fd.SetSizeValue()
	ct.conn.writeCh <- fd
}

func Test_Conn_ServeHTTP_write_error(t *testing.T) {
	ct := newConnTesterNotStarted(t)
	defer ct.Close()
	ct.a.WriteCloser = &failWriter{
		failAtCount: 1,
		failOnWrite: true,
		WriteCloser: ct.a.WriteCloser,
	}
	ct.expectConnError = reflect.TypeOf(errFailWriter)
	ct.expectServerError = reflect.TypeOf(io.ErrUnexpectedEOF)
	ct.Start()
	ct.conn.Ping()
}

func Test_Conn_ServeHTTP_write_close_error(t *testing.T) {
	ct := newConnTesterNotStarted(t)
	defer ct.Close()
	ct.a.WriteCloser = &failWriter{
		failOnClose: true,
		WriteCloser: ct.a.WriteCloser,
	}
	ct.expectConnCloseError = reflect.TypeOf(errFailWriter)
	ct.Start()
	ct.conn.Ping()
}

func Test_Conn_ServeHTTP_write_close_late_error(t *testing.T) {
	ct := newConnTesterNotStarted(t)
	defer ct.Close()
	ct.a.WriteCloser = &failWriter{
		failOnClose: true,
		WriteCloser: ct.a.WriteCloser,
	}
	ct.expectServerError = reflect.TypeOf(io.EOF)
	ct.expectConnError = reflect.TypeOf(errFailWriter)
	ct.expectConnCloseError = reflect.TypeOf(errFailWriter)
	ct.Start()
	ct.conn.writeErrCh <- nil
}

func Test_Conn_stolen_exchange(t *testing.T) {
	ct := newConnTester(t)
	ct.expectConnError = reflect.TypeOf(ErrTimeoutReapingExchanges)
	ct.conn.ReadTimeout = time.Millisecond * 10
	ct.injectFramesAtClose = true
	e := ct.conn.NewExchange()
	assert.NotNil(t, e)
	ct.Close()
	e.Release()
}
