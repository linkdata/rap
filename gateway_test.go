package rap

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

const srvAddr string = "127.0.0.1:10111"

type gwTester struct {
	isClosed bool
}

func (gt *gwTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
}

func Test_Gateway(t *testing.T) {
	gt := &gwTester{}
	srv := &Server{
		Addr:    srvAddr,
		Handler: gt,
	}
	go srv.ListenAndServe()
	gw := NewGateway(srvAddr)
	assert.NotNil(t, gw)

	// send simple request
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	gw.ServeHTTP(rr, r)

	// send request with body
	rr = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", bytes.NewBuffer([]byte{0x20, 0x20}))
	gw.ServeHTTP(rr, r)

	// send request with large body
	rr = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", bytes.NewBuffer(make([]byte, 0x10000)))
	gw.ServeHTTP(rr, r)

	// send request for websocket upgrade
	rr = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Add("Upgrade", "websocket")
	r.Header.Add("Connection", "upgrade")
	gw.ServeHTTP(rr, r)
}
