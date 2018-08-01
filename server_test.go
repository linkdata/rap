package rap

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/stretchr/testify/assert"
)

const srvAddr string = "127.0.0.1:0" // "127.0.0.1:10111"

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

type wrapListener struct {
	net.Listener
	AcceptError   net.Error
	CloseError    error
	listenStarted chan struct{}
}

func (wl *wrapListener) Accept() (net.Conn, error) {
	if wl.listenStarted != nil {
		close(wl.listenStarted)
		wl.listenStarted = nil
	}
	if wl.AcceptError != nil {
		return nil, wl.AcceptError
	}
	return wl.Listener.Accept()
}

func (wl *wrapListener) Close() (err error) {
	err = wl.Listener.Close()
	if err == nil {
		err = wl.CloseError
	}
	return
}

type tempNetError struct {
	counter int
}

func (tne *tempNetError) Error() string {
	return "tempNetError"
}

func (tne *tempNetError) Timeout() bool {
	tne.counter++
	return tne.counter < 5
}

func (tne *tempNetError) Temporary() bool {
	tne.counter++
	return tne.counter < 5
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
	atomic.AddInt64(&st.serveCount, 1)
	if req.Method == "KILL" {
		st.srv.Close()

	} else {
		w.WriteHeader(200)
	}
}

func (st *srvTester) Close() {
	if !st.isClosed {
		st.isClosed = true
		st.srv.Close()
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
	defer leaktest.Check(t)()
	st := newSrvTester(t)
	assert.Equal(t, st.srv.DefaultListenAddr(), st.srv.getListenAddr(""))
	st.Close()
}

func Test_Server_ListenAndServe(t *testing.T) {
	defer leaktest.Check(t)()
	srv := &Server{Addr: "127.0.0.1:"}
	go func() {
		err := srv.ListenAndServe()
		assert.Equal(t, ErrServerClosed, err)
	}()
	assert.NoError(t, srv.Close())

}

func Test_Server_Serve_listen_temporary_error(t *testing.T) {
	srv := &Server{
		Addr: srvAddr,
	}
	ln, lnerr := srv.Listen(srvAddr)
	assert.NoError(t, lnerr)
	assert.NotNil(t, ln)
	srverr := srv.Serve(&wrapListener{Listener: ln, AcceptError: &tempNetError{}})
	srv.Close()
	assert.IsType(t, &tempNetError{}, srverr)
}

func Test_Server_Close_double_close(t *testing.T) {
	srv := &Server{
		Addr: srvAddr,
	}
	ln, lnerr := srv.Listen(srvAddr)
	assert.NoError(t, lnerr)
	assert.NotNil(t, ln)
	srverr := srv.Serve(&wrapListener{Listener: ln, AcceptError: &tempNetError{counter: 10}})
	assert.NoError(t, srv.Close())
	assert.NoError(t, srv.Close())
	assert.IsType(t, &tempNetError{}, srverr)
}

func Test_Server_Close_listener_error(t *testing.T) {
	srv := &Server{
		Addr: srvAddr,
	}
	ln, lnerr := srv.Listen(srvAddr)
	assert.NoError(t, lnerr)
	assert.NotNil(t, ln)
	ls := make(chan struct{})
	wl := &wrapListener{Listener: ln, CloseError: io.ErrUnexpectedEOF, listenStarted: ls}
	go func() {
		srverr := srv.Serve(wl)
		assert.Equal(t, ErrServerClosed, srverr)
	}()
	<-ls
	assert.Equal(t, io.ErrUnexpectedEOF, srv.Close())
}

func Test_Server_support_functions(t *testing.T) {
	defer leaktest.Check(t)()
	st := newSrvTester(t)
	defer st.Close()
	em := st.srv.ServeErrors()
	assert.NotNil(t, em)
	assert.Zero(t, len(em))
	assert.Zero(t, st.srv.ActiveConns())
	assert.Zero(t, st.srv.BytesWritten())
	assert.Zero(t, st.srv.BytesRead())
	st.srv.AddBytesRead(1)
	st.srv.AddBytesWritten(2)
	assert.Equal(t, int64(1), st.srv.BytesRead())
	assert.Equal(t, int64(2), st.srv.BytesWritten())
}

func Test_Server_serve_errors(t *testing.T) {
	defer leaktest.Check(t)()
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("KILL", "/", nil)
	assert.Panics(t, func() { c.ServeHTTP(rr, r) })
	assert.True(t, st.haveServed())
	se := st.srv.ServeErrors()
	assert.NotNil(t, se)
	assert.NotZero(t, len(se))
	assert.Error(t, c.Close())
}
