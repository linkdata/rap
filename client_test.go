package rap

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/stretchr/testify/assert"
)

const noSrvAddr string = "192.0.2.1:1"

func Test_Client_NewClient(t *testing.T) {
	c := NewClient(noSrvAddr)
	assert.NotNil(t, c)
	assert.Zero(t, c.AvailableExchanges())
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
	defer e1.Close()
	e2 := c.NewExchange()
	assert.NotNil(t, e2)
	assert.NotZero(t, c.AvailableExchanges())
	defer e2.Close()
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

	grabbed := make(chan *Exchange, int(ConnExchangeID)*2+1)
	e, err := c.NewExchangeMayDial()
	firstConn := c.getConn()
	assert.NoError(t, err)
	assert.NotNil(t, e)
	assert.Equal(t, int(MaxExchangeID)-1, firstConn.AvailableExchanges())
	for e != nil {
		grabbed <- e
		e = c.NewExchange()
	}
	assert.Equal(t, int(MaxExchangeID), cap(firstConn.exchanges))
	assert.Equal(t, 0, len(firstConn.exchanges))
	assert.Equal(t, 0, firstConn.AvailableExchanges())
	assert.Equal(t, int(MaxExchangeID), len(grabbed))
	e = c.NewExchange()
	assert.Nil(t, e)

	// This will create a new conn since the old is all in use
	e, err = c.NewExchangeMayDial()
	assert.NoError(t, err)
	assert.NotNil(t, e)
	secondConn := c.getConn()
	assert.NotEqual(t, firstConn.serialNumber, secondConn.serialNumber)
	assert.Equal(t, int(MaxExchangeID), cap(secondConn.exchanges))
	assert.Equal(t, int(MaxExchangeID)-1, secondConn.AvailableExchanges())

	// close the exchange, should go back to the secondConn
	e.Close()
	timer1 := time.NewTimer(time.Second * 5)
	defer timer1.Stop()
	for secondConn.AvailableExchanges() != int(MaxExchangeID) {
		select {
		case <-timer1.C:
			assert.FailNow(t, "exchange did not release in time")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	assert.Equal(t, int(MaxExchangeID), secondConn.AvailableExchanges())

	// Now free all the Exchanges in the old conn
	for len(grabbed) > 0 {
		e = <-grabbed
		e.Close()
		assert.NotNil(t, e)
		assert.Equal(t, firstConn.serialNumber, e.getConn().serialNumber)
	}

	// Grab all available exchanges, should be two conn's worth available eventually
	e = c.NewExchange()
	assert.NotNil(t, e)
	timer2 := time.NewTimer(time.Second * 5)
	defer timer2.Stop()
	for len(grabbed) < int(MaxExchangeID)*2 {
		if e != nil {
			grabbed <- e
		} else {
			select {
			case <-timer2.C:
				assert.FailNow(t, fmt.Sprint("unable to grab ", int(MaxExchangeID)*2, " exchanges, got ", len(grabbed)))
			default:
			}
		}
		e = c.NewExchange()
		// log.Print("grabbed ", len(grabbed), " e=", e)
	}

	assert.Equal(t, int(MaxExchangeID)*2, len(grabbed))
	for len(grabbed) > 0 {
		(<-grabbed).Close()
	}
}

func Test_Client_ServeHTTP_simple(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}
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

func Test_Client_ServeHTTP_aborted_by_server(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	rr := httptest.NewRecorder()
	blob := make([]byte, FrameMaxSize*(SendWindowSize*2))
	for n := range blob {
		blob[n] = 0xAB
	}
	req := httptest.NewRequest("ABORTBODY", "/abortme", ioutil.NopCloser(bytes.NewBuffer(blob)))
	req.ContentLength = int64(len(blob))
	c.ServeHTTP(rr, req)
	assert.True(t, st.haveServed())
	assert.Equal(t, 400, rr.Result().StatusCode)
	assert.NoError(t, c.Close())
}

func Test_Client_ServeHTTP_overflow_headers(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("NotReallyAMethod", "/overflow", nil)
	req.ContentLength = 0
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

func Test_Client_ServeHTTP_no_answer(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}
	c := NewClient(noSrvAddr)
	c.DialTimeout = time.Millisecond * 10
	rr := httptest.NewRecorder()
	c.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, http.StatusGatewayTimeout, rr.Code)
	assert.NoError(t, c.Close())
}

func Test_Client_ServeHTTP_websocket_simple(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	defer assert.NoError(t, c.Close())

	ts := httptest.NewServer(c)
	defer ts.Close()
}

func Test_Client_parallel_queries(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	// race detector limit is 8192 goroutines
	parallelism := int32((8192 - runtime.NumGoroutine()) / 3)
	factor := int32(MaxExchangeID*4) / parallelism
	queryCount := parallelism * factor
	serveCount := int32(0)

	s := &Server{
		Addr:         srvAddr,
		ReadTimeout:  time.Second * 5,
		WriteTimeout: time.Second * 5,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&serveCount, 1)
			w.WriteHeader(200)
		}),
	}
	ln, lnerr := s.Listen(srvAddr)
	assert.NoError(t, lnerr)
	defer s.Close()
	go s.Serve(ln)

	c := NewClient(s.Addr)
	c.ReadTimeout = time.Second * 10
	c.WriteTimeout = time.Second * 10
	defer c.Close()
	wg := sync.WaitGroup{}
	for i := parallelism; i > 0; i-- {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := factor; i > 0; i-- {
				rr := httptest.NewRecorder()
				r := httptest.NewRequest("GET", "/", nil)
				c.ServeHTTP(rr, r)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, queryCount, serveCount)
}
