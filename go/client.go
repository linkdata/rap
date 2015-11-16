package rap

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Client connects to a RAP server, maintaining one or more Conns.
type Client struct {
	Addr         string     // where to connect
	conn         *Conn      // the current active connection (atomic access only)
	mu           sync.Mutex // protects those below
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

// dial creates a new RAP Conn to the server.
// Must run with the mutex locked.
func (c *Client) dial() *Conn {
	rwc, err := net.Dial("tcp", c.Addr)
	if err != nil {
		c.lastError = err
		c.lastAttempt = time.Now()
		if c.firstAttempt.IsZero() {
			c.firstAttempt = c.lastAttempt
		}
		log.Print("Client.dial(): ", err.Error())
		return nil
	}
	c.lastError = nil
	c.lastAttempt = time.Time{}
	c.firstAttempt = time.Time{}
	conn := NewConn(rwc)
	go conn.Serve(nil)
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

// NewExchange returns a new Exchange for use, or nil if none available.
func (c *Client) NewExchange() *Exchange {
	if conn := c.getConn(); conn != nil {
		return conn.NewExchange()
	}
	return nil
}

func (c *Client) offlineError() error {
	if c.lastError == nil {
		return nil
	}
	if c.firstAttempt == c.lastAttempt {
		return c.lastError
	}
	return fmt.Errorf("Upstream server has been unresponsive for %v, last error was:\r\n\"%v\"\r\n",
		time.Now().Sub(c.firstAttempt), c.lastError)
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
				bestConn = c.dial()
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
