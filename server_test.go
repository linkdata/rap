package rap

import (
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type srvTester struct {
	t          *testing.T
	isClosed   bool
	srv        *Server
	serveCount int64
	serveDone  chan struct{}
	serveErr   error
}

func newSrvTester(t *testing.T) *srvTester {
	st := &srvTester{
		t: t,
		srv: &Server{
			Addr: srvAddr,
		},
		serveDone: make(chan struct{}),
	}
	st.srv.Handler = st
	ln, lnerr := st.srv.Listen(srvAddr)
	assert.NoError(t, lnerr)
	assert.NotNil(t, ln)
	go st.Serve(ln)
	return st
}

func (st *srvTester) haveServed() bool {
	return atomic.LoadInt64(&st.serveCount) > 0
}

func (st *srvTester) Serve(ln net.Listener) {
	st.serveErr = st.srv.Serve(ln)
	assert.Equal(st.t, ErrServerClosed, st.serveErr)
	close(st.serveDone)
}

func (st *srvTester) WaitForServed() bool {
	ticker := time.NewTicker(time.Millisecond * 10)
	defer ticker.Stop()

	for ticks := 0; ticks < 10; ticks++ {
		if st.haveServed() {
			return true
		}
		<-ticker.C
	}
	return false
}

func (st *srvTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
	atomic.AddInt64(&st.serveCount, 1)
}

func (st *srvTester) Close() {
	if !st.isClosed {
		st.isClosed = true
		st.srv.Close()
		// st.srv.Shutdown(context.Background())
		timer := time.NewTimer(time.Second * 5)
		defer timer.Stop()
		select {
		case <-st.serveDone:
		case <-timer.C:
			assert.NoError(st.t, errors.New("server_test: Timeout waiting for server to stop"))
		}
	}
}

func Test_Server_simple(t *testing.T) {
	st := newSrvTester(t)
	st.Close()
}

func Test_Server_support_functions(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	em := st.srv.ServeErrors()
	assert.NotNil(t, em)
	assert.Zero(t, st.srv.ActiveConns())
	assert.Zero(t, st.srv.BytesWritten())
	assert.Zero(t, st.srv.BytesRead())
	st.srv.AddBytesRead(1)
	st.srv.AddBytesWritten(2)
	assert.Equal(t, int64(1), st.srv.BytesRead())
	assert.Equal(t, int64(2), st.srv.BytesWritten())
}

/*
func Test_Server_serve_errors(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	gw := NewGateway(srvAddr)
	assert.NotNil(t, gw)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	gw.ServeHTTP(rr, r)
	assert.True(t, st.WaitForServed())
	assert.NotNil(t, st.srv)
	assert.NotNil(t, st.srv.listener)
	assert.NotNil(t, gw.Client)
	conn := gw.Client.getConn()
	assert.NotNil(t, conn)
	if conn != nil {
		conn.CloseWrite()
	}
	rr = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", nil)
	gw.ServeHTTP(rr, r)
	assert.NotNil(t, st.srv)
	assert.NotNil(t, st.srv.listener)
	st.srv.listener.Close()
	em := st.srv.ServeErrors()
	assert.Equal(t, 1, len(em))
}
*/
