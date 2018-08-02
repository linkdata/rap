package rap

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Client connects to a RAP server, maintaining one or more Conns.
type Client struct {
	Addr         string        // where to connect
	DialTimeout  time.Duration // dialing timeout
	conn         *Conn         // the current active connection (atomic access only)
	mu           sync.Mutex    // protects those below
	lastError    error
	lastAttempt  time.Time
	firstAttempt time.Time
	conns        []*Conn
}

// NewClient starts a new RAP Client. The Client will make establish Conn's
// to the RAP Server at the given address as needed. This implies that no
// connection will be made immediately.
func NewClient(addr string) *Client {
	return &Client{
		Addr:  addr,
		conns: make([]*Conn, 0),
	}
}

// Close closes all connections.
func (c *Client) Close() (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, conn := range c.conns {
		c.conns[i] = nil
		if conn != nil {
			if connerr := conn.Close(); err == nil {
				err = connerr
			}
		}
	}
	c.conns = nil
	return
}

// dialLocked creates a new RAP Conn to the server.
// Must run with the mutex locked.
func (c *Client) dialLocked() *Conn {
	rwc, err := net.DialTimeout("tcp", c.Addr, c.DialTimeout)
	if err != nil {
		c.lastError = err
		c.lastAttempt = time.Now()
		if c.firstAttempt.IsZero() {
			c.firstAttempt = c.lastAttempt
		}
		// log.Print("Client.dial(): ", err.Error())
		return nil
	}
	c.lastError = nil
	c.lastAttempt = time.Time{}
	c.firstAttempt = time.Time{}
	conn := NewConn(rwc)
	go conn.ServeHTTP(nil)
	c.conns = append(c.conns, conn)
	return conn
}

// selectBestConn returns an existing Conn that have free Exchanges,
// or nil if none were found.
// Must run with the mutex locked.
func (c *Client) selectBestConn() (bestConn *Conn) {
	bestLength := 0
	for _, conn := range c.conns {
		if len(conn.exchanges) > bestLength {
			bestLength = len(conn.exchanges)
			bestConn = conn
		}
	}
	return
}

// non-racy "return c.conn"
func (c *Client) getConn() *Conn {
	return (*Conn)(atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(&c.conn))))
}

// non-racy "c.conn = conn"
func (c *Client) setConn(conn *Conn) {
	atomic.StorePointer((*unsafe.Pointer)(unsafe.Pointer(&c.conn)), unsafe.Pointer(conn))
}

func (c *Client) offlineError() (err error) {
	if err = c.lastError; err == nil {
		err = fmt.Errorf("upstream server unresponsive")
	}
	if c.firstAttempt != c.lastAttempt {
		err = fmt.Errorf("%v; no response for %v",
			err, time.Now().Sub(c.firstAttempt))
	}
	return
}

// NewExchange returns a new Exchange for use, or nil if none available.
func (c *Client) NewExchange() (e *Exchange) {
	if conn := c.getConn(); conn != nil {
		e = conn.NewExchange()
	}
	if e == nil {
		bestConn := c.selectBestConn()
		if bestConn != nil {
			if e = bestConn.NewExchange(); e != nil {
				c.setConn(bestConn)
			}
		}
	}
	return
}

// NewExchangeMayDial will find a Conn with free Exchanges or
// create a new one if needed.
func (c *Client) NewExchangeMayDial() (e *Exchange, err error) {
	startTime := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for e == nil {
		bestConn := c.selectBestConn()
		if bestConn == nil {
			// not enough free, make a new connection
			if c.lastAttempt.Before(startTime) {
				bestConn = c.dialLocked()
			}
			if bestConn == nil {
				return nil, c.offlineError()
			}
		}
		// grab an exchange before we publish the new conn
		if e = bestConn.NewExchangeWait(time.Millisecond * 100); e != nil {
			c.setConn(bestConn)
		}
	}
	return e, nil
}

func (c *Client) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e := c.NewExchange()

	if e == nil {
		var err error
		e, err = c.NewExchangeMayDial()
		if e == nil {
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
	}

	defer e.Release() // will do Stop() before release

	/*
		isWebsocketRequest := false

		// Detect and handle WebSocket requests.
		if len(r.Header["Upgrade"]) > 0 &&
			len(r.Header["Connection"]) > 0 &&
			r.ProtoAtLeast(1, 1) &&
			r.Method == "GET" &&
			strings.ToLower(r.Header["Upgrade"][0]) == "websocket" &&
			strings.ToLower(r.Header["Connection"][0]) == "upgrade" {
			isWebsocketRequest = true

			hj, ok := w.(http.Hijacker)
			if !ok {
				panic("rap.Client.ServeHTTP(): http.Hijacker unsupported")
			}
			rwc, buf, err := hj.Hijack()
			if err != nil {
				panic(err.Error())
			}
			defer rwc.Close()
			br := buf.Reader
			if br.Buffered() > 0 {
				panic("rap.Client.ServeHTTP(): websocket client sent data before handshake was complete")
			}
			e.initiateWebsocket(rwc, buf, r)
		}
	*/

	var requestErr error
	var responseErr error

	if r.ContentLength == 0 {
		// we can run this without a separate goroutine as request has no body.
		requestErr = e.WriteRequest(r)
		responseErr = e.ProxyResponse(w)
	} else {
		// we allow a response to send before request has finished sending,
		// useful when the upstream does stream processing (such as echoing).
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			if requestErr = e.WriteRequest(r); requestErr != nil {
				e.Close()
			}
		}()
		responseErr = e.ProxyResponse(w)
		wg.Wait()
	}

	if responseErr == nil && requestErr == nil {
		return
	}

	errorText := "rap.Client.ServeHTTP():"
	if requestErr != nil {
		errorText += " requestErr=" + requestErr.Error()
	}
	if responseErr != nil {
		errorText += " responseErr=" + responseErr.Error()
	}
	panic(errorText)
}
