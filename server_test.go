package rap

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_Server_support_functions(t *testing.T) {
	s := &Server{}
	em := s.ServeErrors()
	assert.NotNil(t, em)
	assert.Zero(t, s.ActiveConns())
	assert.Zero(t, s.BytesWritten())
	assert.Zero(t, s.BytesRead())
	s.AddBytesRead(1)
	s.AddBytesWritten(2)
	assert.Equal(t, int64(1), s.BytesRead())
	assert.Equal(t, int64(2), s.BytesWritten())
}

func Test_Server_serve_errors(t *testing.T) {
	gt := &gwTester{}
	srv := &Server{
		Addr:    srvAddr,
		Handler: gt,
	}
	go srv.ListenAndServe()
	gw := NewGateway(srvAddr)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	gw.ServeHTTP(rr, r)
	gw.Client.getConn().CloseWrite()
	rr = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", nil)
	gw.ServeHTTP(rr, r)
	srv.listener.Close()
	em := srv.ServeErrors()
	assert.Equal(t, 1, len(em))
}
