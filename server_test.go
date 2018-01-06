package rap

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type srvTester struct {
	t          *testing.T
	isClosed   bool
	srv        *Server
	isServed   bool
	haveServed chan struct{}
	serveDone  chan struct{}
	serveErr   error
}

func newSrvTester(t *testing.T) *srvTester {
	st := &srvTester{
		t: t,
		srv: &Server{
			Addr: srvAddr,
		},
		haveServed: make(chan struct{}),
		serveDone:  make(chan struct{}),
	}
	st.srv.Handler = st
	go st.Serve()
	return st
}

func (st *srvTester) Serve() {
	st.serveErr = st.srv.ListenAndServe()
	close(st.serveDone)
}

func (st *srvTester) WaitForServed() bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-st.haveServed:
		return true
	case <-timer.C:
	}
	return false
}

func (st *srvTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
	if !st.isServed {
		st.isServed = true
		close(st.haveServed)
	}
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
