package rap

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const srvAddr string = "127.0.0.1:0" // "127.0.0.1:10111"

type gwTester struct {
	t          *testing.T
	st         *srvTester
	isServed   bool
	haveServed chan struct{}
}

func newGWTester(t *testing.T) *gwTester {
	return &gwTester{
		t: t,
		// st:         newSrvTester(t),
		haveServed: make(chan struct{}),
	}
}

func (gt *gwTester) Close() {
	gt.st.Close()
}

func (gt *gwTester) WaitForServed() bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-gt.haveServed:
		return true
	case <-timer.C:
	}
	return false
}

func (gt *gwTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
	if !gt.isServed {
		gt.isServed = true
		close(gt.haveServed)
	}
}

func Test_Gateway_NewGateway(t *testing.T) {
	gw := NewGateway(srvAddr)
	assert.NoError(t, gw.Close())
}

func Test_Gateway_ServeHTTP(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	gw := NewGateway(st.srv.Addr)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	gw.ServeHTTP(rr, r)
	assert.True(t, st.haveServed())
	assert.NoError(t, gw.Close())
}

func Test_Gateway_ServeHTTP_with_body(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	gw := NewGateway(st.srv.Addr)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/foo/?bar=quux&bar=foo", ioutil.NopCloser(bytes.NewBufferString("Hello world body!")))
	req.ContentLength = -1
	gw.ServeHTTP(rr, req)
	assert.True(t, st.haveServed())
	assert.NoError(t, gw.Close())
}

func Test_Gateway_ServeHTTP_overflow_headers(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	gw := NewGateway(st.srv.Addr)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("NotReallyAMethod", "/overflow", nil)
	req.ContentLength = -1
	for i := 0; i < 8000; i++ {
		req.Header.Add(fmt.Sprint("Header", i), fmt.Sprint("Value", i))
	}
	assert.Panics(t, func() { gw.ServeHTTP(rr, req) })
	assert.False(t, st.haveServed())
	assert.NoError(t, gw.Close())
}

func Test_Gateway_no_answer(t *testing.T) {
	gw := NewGateway(noSrvAddr)
	gw.Client.DialTimeout = time.Millisecond * 10
	// send simple request
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	gw.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusGatewayTimeout, rr.Code)
}

func Test_Gateway_simple(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	/*
		gt := newGWTester(t)
		srv := &Server{
			Handler: gt,
		}
		ln, err := srv.Listen(srvAddr)
		assert.NoError(t, err)
		srv.listener = ln
		go func() {
			assert.Error(t, srv.ListenAndServe())
		}()
		defer ln.Close()
	*/
	/*
		gw := NewGateway(srvAddr)
		assert.NotNil(t, gw)

		// send simple request
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		gw.ServeHTTP(rr, r)
		assert.Equal(t, http.StatusOK, rr.Code)

		// send request with body
		rr = httptest.NewRecorder()
		r = httptest.NewRequest("GET", "/", bytes.NewBuffer([]byte{0x20, 0x20}))
		gw.ServeHTTP(rr, r)
		assert.Equal(t, http.StatusOK, rr.Code)

		// send request with large body
		rr = httptest.NewRecorder()
		r = httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, 0x10000)))
		gw.ServeHTTP(rr, r)
		assert.Equal(t, http.StatusOK, rr.Code)

		// send request for websocket upgrade
		// fails since http hijacker not supported by httptest.ResponseRecorder
		rr = httptest.NewRecorder()
		r = httptest.NewRequest("GET", "/", nil)
		r.Header.Add("Upgrade", "websocket")
		r.Header.Add("Connection", "upgrade")
		gw.ServeHTTP(rr, r)
		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	*/
}
