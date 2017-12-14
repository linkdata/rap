package rap

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	*io.PipeReader
	*io.PipeWriter
	bytesWritten int64
	bytesRead    int64
}

func (rwcp *rwcPipe) Close() error {
	if err := rwcp.PipeWriter.Close(); err != nil {
		panic(err)
	}
	return rwcp.PipeReader.Close()
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
		PipeReader: rb,
		PipeWriter: wa,
	}
	b = &rwcPipe{
		PipeReader: ra,
		PipeWriter: wb,
	}
	return
}

type connTester struct {
	t          *testing.T
	a, b       *rwcPipe
	conn       *Conn
	server     *Conn
	isClosed   bool
	serverDone chan struct{}
	connDone   chan struct{}
}

func (ct *connTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
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
		assert.NoError(ct.t, ct.a.PipeWriter.Close())
		assert.NoError(ct.t, ct.b.PipeWriter.Close())
		<-ct.serverDone
		<-ct.connDone
	}
}

func newConnTester(t *testing.T) (ct *connTester) {
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
	go func(ct *connTester) {
		err := ct.server.ServeHTTP(ct)
		close(ct.serverDone)
		if !ct.isClosed {
			panic(fmt.Sprint("ct.server.ServeHTTP(ct) premature exit: ", err))
		}
	}(ct)
	go func(ct *connTester) {
		err := ct.conn.ServeHTTP(nil)
		close(ct.connDone)
		if !ct.isClosed {
			panic(fmt.Sprint("ct.conn.ServeHTTP(ct) premature exit: ", err))
		}
	}(ct)
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

func Test_Conn_ping_pong(t *testing.T) {
	ct := newConnTester(t)
	defer ct.Close()
	assert.Zero(t, ct.conn.lastPingSent)
	assert.Zero(t, ct.conn.lastPongRcvd)
	assert.Zero(t, ct.server.lastPingSent)
	assert.Zero(t, ct.server.lastPongRcvd)
	ct.conn.Ping()
	for ct.conn.lastPongRcvd.IsZero() {
		time.Sleep(10 * time.Millisecond)
	}
	assert.NotZero(t, ct.conn.lastPingSent)
	assert.NotZero(t, ct.conn.lastPongRcvd)
	assert.Zero(t, ct.server.lastPingSent)
	assert.Zero(t, ct.server.lastPongRcvd)
	// assert.NotZero(t, ct.conn.latency)
}
