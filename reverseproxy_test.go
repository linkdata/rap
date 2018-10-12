package rap

import (
	"bytes"
	"io/ioutil"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_ReverseProxy_NewReverseProxy(t *testing.T) {
	rp := NewReverseProxy(&url.URL{}, 0)
	assert.NotNil(t, rp)
	assert.NotZero(t, cap(rp.transports))
}

func Test_ReverseProxy_ServeHTTP(t *testing.T) {
	et := newConnTester(t)
	defer et.Close()
	hs := httptest.NewUnstartedServer(et)
	hs.Config.Addr = "127.0.0.1:"
	hs.Start()
	defer hs.Close()
	u, err := url.Parse(hs.URL)
	assert.NoError(t, err)
	rp := NewReverseProxy(u, 0)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/foo/?bar=quux&bar=foo", ioutil.NopCloser(bytes.NewBufferString("Hello world body!")))
	req.Header.Add("Connection", "keep-alive")
	req.ContentLength = -1
	rp.ServeHTTP(rr, req)
}
