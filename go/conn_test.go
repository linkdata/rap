package rap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/stretchr/testify/assert"
)

type rwcPipe struct {
	*io.PipeReader
	*io.PipeWriter
}

const connTimeout = time.Millisecond * time.Duration(10)

func (rwcp *rwcPipe) Close() error {
	if err := rwcp.PipeWriter.Close(); err != nil {
		panic(err)
	}
	return rwcp.PipeReader.Close()
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
	go func(ct *connTester) {
		err := ct.server.ServeHTTP(ct)
		ct.t.Log("ct.server.ServeHTTP(ct)", err)
		// assert.Equal(ct.t, io.ErrClosedPipe, err)
		close(ct.serverDone)
	}(ct)
	go func(ct *connTester) {
		err := ct.conn.ServeHTTP(nil)
		ct.t.Log("ct.conn.ServeHTTP(ct)", err)
		// assert.Equal(ct.t, io.ErrClosedPipe, err)
		close(ct.connDone)
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

func Test_Conn_normal_request_response(t *testing.T) {
	defer leaktest.Check(t)()
	ct := newConnTester(t)
	defer ct.Close()
	ct.InjectRequest(httptest.NewRequest("GET", "/", nil))
}
