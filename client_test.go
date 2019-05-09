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
	assert.Zero(t, c.AvailableConns())
	defer c.Close()
}

func Test_Client_no_answer(t *testing.T) {
	c := NewClient(noSrvAddr)
	defer c.Close()
	c.DialTimeout = time.Millisecond * 10
	conn, err := c.NewConnMayDial()
	assert.Nil(t, conn)
	assert.Error(t, err)
}

func Test_Client_server_seems_offline(t *testing.T) {
	c := NewClient(noSrvAddr)
	defer c.Close()
	assert.Error(t, c.offlineError())
	c.DialTimeout = time.Millisecond * 10
	c.firstAttempt = time.Now().Add(-time.Second)
	conn, err := c.NewConnMayDial()
	assert.Nil(t, conn)
	assert.Error(t, err)
}

func Test_Client_dial_and_close(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	defer c.Close()
	assert.NotNil(t, c)
	c.DialTimeout = time.Second
	c1, err := c.NewConnMayDial()
	assert.NoError(t, err)
	assert.NotNil(t, c1)
	defer c1.Close()
	c2 := c.NewConn()
	assert.NotNil(t, c2)
	assert.NotZero(t, c.AvailableConns())
	defer c2.Close()
	if c1 != nil && c2 != nil {
		assert.Equal(t, c1.mux, c2.mux)
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

func Test_Client_exhaust_muxer(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(st.srv.Addr)
	assert.NotNil(t, c)
	defer c.Close()
	c.DialTimeout = time.Second

	grabbed := make(chan *Conn, int(MuxerConnID)*2+1)
	conn, err := c.NewConnMayDial()
	firstMux := c.getMux()
	assert.NoError(t, err)
	assert.NotNil(t, conn)
	assert.Equal(t, int(MaxConnID)-1, firstMux.AvailableConns())
	for conn != nil {
		grabbed <- conn
		conn = c.NewConn()
	}
	assert.Equal(t, int(MaxConnID), cap(firstMux.conns))
	assert.Equal(t, 0, len(firstMux.conns))
	assert.Equal(t, 0, firstMux.AvailableConns())
	assert.Equal(t, int(MaxConnID), len(grabbed))
	conn = c.NewConn()
	assert.Nil(t, conn)

	// This will create a new Muxer since the old is all in use
	conn, err = c.NewConnMayDial()
	assert.NoError(t, err)
	assert.NotNil(t, conn)
	secondMux := c.getMux()
	assert.NotEqual(t, firstMux.serialNumber, secondMux.serialNumber)
	assert.Equal(t, int(MaxConnID), cap(secondMux.conns))
	assert.Equal(t, int(MaxConnID)-1, secondMux.AvailableConns())

	// close the Conn, should go back to the secondMux
	conn.Close()
	timer1 := time.NewTimer(time.Second * 5)
	defer timer1.Stop()
	for secondMux.AvailableConns() != int(MaxConnID) {
		select {
		case <-timer1.C:
			assert.FailNow(t, "Conn did not release in time")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	assert.Equal(t, int(MaxConnID), secondMux.AvailableConns())

	// Now free all the Conns in the old mux
	for len(grabbed) > 0 {
		conn = <-grabbed
		conn.Close()
		assert.NotNil(t, conn)
		assert.Equal(t, firstMux.serialNumber, conn.getMux().serialNumber)
	}

	// Grab all available conns, should be two Muxers worth available eventually
	conn = c.NewConn()
	assert.NotNil(t, conn)
	timer2 := time.NewTimer(time.Second * 5)
	defer timer2.Stop()
	for len(grabbed) < int(MaxConnID)*2 {
		if conn != nil {
			grabbed <- conn
		} else {
			select {
			case <-timer2.C:
				assert.FailNow(t, fmt.Sprint("unable to grab ", int(MaxConnID)*2, " conns, got ", len(grabbed)))
			default:
			}
		}
		conn = c.NewConn()
		// log.Print("grabbed ", len(grabbed), " conn=", conn)
	}

	assert.Equal(t, int(MaxConnID)*2, len(grabbed))
	for len(grabbed) > 0 {
		(<-grabbed).Close()
	}
}

func Test_Client_parallel_queries(t *testing.T) {
	if leaktestEnabled {
		defer leaktest.Check(t)()
	}

	// race detector limit is 8192 goroutines
	parallelism := int32((8192 - runtime.NumGoroutine()) / 3)
	factor := int32(MaxConnID*4) / parallelism
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
