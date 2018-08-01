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

const noSrvAddr string = "192.0.2.1:1"

func Test_Client_NewClient(t *testing.T) {
	c := NewClient(noSrvAddr)
	assert.NotNil(t, c)
	defer c.Close()
}

func Test_Client_no_answer(t *testing.T) {
	c := NewClient(noSrvAddr)
	defer c.Close()
	c.DialTimeout = time.Millisecond * 10
	e, err := c.NewExchangeMayDial()
	assert.Nil(t, e)
	assert.Error(t, err)
}

func Test_Client_server_seems_offline(t *testing.T) {
	c := NewClient(noSrvAddr)
	defer c.Close()
	assert.Error(t, c.offlineError())
	c.DialTimeout = time.Millisecond * 10
	c.firstAttempt = time.Now().Add(-time.Second)
	e, err := c.NewExchangeMayDial()
	assert.Nil(t, e)
	assert.Error(t, err)
}

func Test_Client_connect_and_close(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	defer c.Close()
	assert.NotNil(t, c)
	c.DialTimeout = time.Second
	e1, err := c.NewExchangeMayDial()
	assert.NoError(t, err)
	assert.NotNil(t, e1)
	defer e1.Release()
	e2 := c.NewExchange()
	assert.NotNil(t, e2)
	defer e2.Release()
	if e1 != nil && e2 != nil {
		assert.Equal(t, e1.conn, e2.conn)
	}
}

func Test_Client_exhaust_conn(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	assert.NotNil(t, c)
	defer c.Close()
	c.DialTimeout = time.Second

	grabbed := make(chan *Exchange, MaxExchangeID*2+1)
	e, err := c.NewExchangeMayDial()
	firstConn := c.getConn()
	assert.NoError(t, err)
	assert.NotNil(t, e)
	for e != nil {
		grabbed <- e
		e = c.NewExchange()
	}
	if firstConn != nil {
		assert.Equal(t, int(MaxExchangeID), cap(firstConn.exchanges))
		assert.Equal(t, 0, len(firstConn.exchanges))
	}
	e = c.NewExchange()
	assert.Nil(t, e)

	// This will create a new conn since the old is all in use
	e, err = c.NewExchangeMayDial()
	assert.NoError(t, err)
	assert.NotNil(t, e)
	secondConn := c.getConn()
	e.Release()

	if secondConn != nil {
		assert.NotEqual(t, len(firstConn.exchanges), len(secondConn.exchanges))
		assert.Equal(t, int(MaxExchangeID), cap(secondConn.exchanges))
		assert.Equal(t, 1, len(secondConn.exchanges))
	}

	// Now free all the Exchanges in the old conn
releaseOldConn:
	for {
		select {
		case e = <-grabbed:
			e.Release()
			assert.NotNil(t, e)
			assert.Equal(t, firstConn, e.conn)
		default:
			break releaseOldConn
		}
	}

	// Grab all available exchanges, should be two conn's worth
	e = c.NewExchange()
	assert.NotNil(t, e)
	for e != nil {
		grabbed <- e
		e = c.NewExchange()
	}

	assert.Equal(t, int(MaxExchangeID)*2, len(grabbed))
	for len(grabbed) > 0 {
		(<-grabbed).Release()
	}
}

func Test_Client_ServeHTTP(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	c.ServeHTTP(rr, r)
	assert.True(t, st.haveServed())
	assert.NoError(t, c.Close())
}

func Test_Client_ServeHTTP_with_body(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/foo/?bar=quux&bar=foo", ioutil.NopCloser(bytes.NewBufferString("Hello world body!")))
	req.ContentLength = -1
	c.ServeHTTP(rr, req)
	assert.True(t, st.haveServed())
	assert.NoError(t, c.Close())
}

func Test_Client_ServeHTTP_overflow_headers(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("NotReallyAMethod", "/overflow", nil)
	req.ContentLength = -1
	for i := 0; i < 8000; i++ {
		req.Header.Add(fmt.Sprint("Header", i), fmt.Sprint("Value", i))
	}
	assert.Panics(t, func() { c.ServeHTTP(rr, req) })
	assert.False(t, st.haveServed())
	se := st.srv.ServeErrors()
	assert.NotNil(t, se)
	assert.Zero(t, len(se))
	assert.NoError(t, c.Close())
}

func Test_Client_ServeHTTP_websocket_missing_hijack(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Add("Upgrade", "websocket")
	req.Header.Add("Connection", "upgrade")
	assert.Panics(t, func() { c.ServeHTTP(rr, req) })
	assert.False(t, st.haveServed())
	assert.NoError(t, c.Close())
}

func Test_Client_ServeHTTP_no_answer(t *testing.T) {
	c := NewClient(noSrvAddr)
	c.DialTimeout = time.Millisecond * 10
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Add("Upgrade", "websocket")
	req.Header.Add("Connection", "upgrade")
	c.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusGatewayTimeout, rr.Code)
	assert.NoError(t, c.Close())
}
