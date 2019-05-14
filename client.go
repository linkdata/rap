// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

package rap

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

// Client dials a RAP server, maintaining zero or more Muxers.
type Client struct {
	Addr         string        // the address to dial
	DialTimeout  time.Duration // dialing timeout
	ReadTimeout  time.Duration // read timeout (reading the request)
	WriteTimeout time.Duration // write timeout (writing the response)
	netLog       bool          // set to true to log.Print() network data
	mux          *Muxer        // the current active Muxer (atomic access only)
	paused       int32
	mu           sync.Mutex // protects those below
	lastError    error
	lastAttempt  time.Time
	firstAttempt time.Time
	muxers       []*Muxer
}

// NewClient starts a new RAP Client. The Client will establish network connections
// to the RAP Server at the given address as needed. This implies that no
// network connection will be made immediately.
func NewClient(addr string) *Client {
	return &Client{
		Addr:        addr,
		DialTimeout: time.Second * 60,
		muxers:      make([]*Muxer, 0),
	}
}

func (c *Client) closeMuxersLocked() (err error) {
	for i, mux := range c.muxers {
		c.muxers[i] = nil
		if mux != nil {
			if muxerr := mux.Close(); err == nil {
				err = muxerr
			}
		}
	}
	return
}

func (c *Client) closeMuxers() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeMuxersLocked()
}

// Close stops serving requests and closes all Muxers immediately.
func (c *Client) Close() (err error) {
	atomic.StoreInt32(&c.paused, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeMuxersLocked()
	c.muxers = nil
	return
}

func (c *Client) shutdownMuxers() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, mux := range c.muxers {
		c.muxers[i] = nil
		if mux != nil {
			if err := mux.Shutdown(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Shutdown attempts a graceful shutdown of the client.
// It immediately stops accepting new requests, and then
// it calls Shutdown() on all of it's muxers.
// If any of those fail, it simply closes all muxers.
func (c *Client) Shutdown() error {
	atomic.StoreInt32(&c.paused, 1)
	if err := c.shutdownMuxers(); err != nil {
		c.closeMuxers()
		return err
	}
	return nil
}

// dialLocked creates a new RAP Muxer to the server.
// Must run with the mutex locked.
func (c *Client) dialLocked() *Muxer {
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
	mux := NewMuxer(rwc)
	mux.netLog = c.netLog
	go mux.ServeHTTP(nil)
	c.muxers = append(c.muxers, mux)
	return mux
}

// NetLog enables or disables logging of network data
// and Conn state changes. This is a large volume of
// information, so it's recommended to redirect the
// log output using log.SetOutput().
func (c *Client) NetLog(state bool) {
	c.netLog = state
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, mux := range c.muxers {
		if mux != nil {
			mux.NetLog(state)
		}
	}
}

// selectBestMuxLocked returns an existing Muxer that have free Conns,
// or nil if none were found.
// Must run with the mutex locked.
func (c *Client) selectBestMuxLocked() (bestMux *Muxer) {
	bestLength := 0
	for _, mux := range c.muxers {
		avail := mux.AvailableConns()
		if avail > bestLength {
			bestLength = avail
			bestMux = mux
		}
	}
	return
}

func (c *Client) getMux() *Muxer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getMuxLocked()
}

func (c *Client) getMuxLocked() *Muxer {
	return c.mux
}

func (c *Client) setMuxLocked(mux *Muxer) {
	c.mux = mux
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

// NewConn returns a new Conn for use, or nil if none available.
func (c *Client) NewConn() (conn *Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mux := c.getMuxLocked(); mux != nil {
		conn = mux.NewConn()
	}
	if conn == nil {
		bestMux := c.selectBestMuxLocked()
		if bestMux != nil {
			if conn = bestMux.NewConn(); conn != nil {
				c.setMuxLocked(bestMux)
			}
		}
	}
	return
}

// AvailableConns returns the number of Conns currently not
// serving a request. They may be distributed across many Muxers.
func (c *Client) AvailableConns() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.availableConnsLocked()
}

func (c *Client) availableConnsLocked() (connCount int) {
	for _, mux := range c.muxers {
		connCount += mux.AvailableConns()
	}
	return
}

// NewConnMayDial will find a Muxer with free Conns or
// create a new one if needed.
func (c *Client) NewConnMayDial() (conn *Conn, err error) {
	startTime := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for conn == nil {
		bestMux := c.selectBestMuxLocked()
		if bestMux == nil {
			// not enough free, dial a new one
			if c.lastAttempt.Before(startTime) {
				bestMux = c.dialLocked()
			}
			if bestMux == nil {
				return nil, c.offlineError()
			}
		}
		// grab a Conn before we publish the new Muxer
		if conn = bestMux.NewConnWait(c.DialTimeout); conn != nil {
			c.setMuxLocked(bestMux)
		}
	}
	return conn, nil
}

func (c *Client) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if atomic.LoadInt32(&c.paused) != 0 {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	conn := c.NewConn()
	if conn == nil {
		var err error
		conn, err = c.NewConnMayDial()
		if conn == nil {
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
	}
	c.serveHTTP(w, r, conn)
}

func (c *Client) serveHTTP(w http.ResponseWriter, r *http.Request, conn *Conn) {
	defer conn.Close()

	var requestErr error
	var responseErr error

	// log.Printf("rap.Client.ServeHTTP(): %+v\n", r)

	isUpgrade := false
	if vals, ok := r.Header["Connection"]; ok {
		for _, val := range vals {
			if strings.ToLower(val) == "upgrade" {
				isUpgrade = true
				break
			}
		}
	}

	var statusCode int

	if c.ReadTimeout != 0 {
		conn.SetDeadline(time.Now().Add(c.ReadTimeout))
	}

	requestErr = conn.WriteRequest(r)

	if c.WriteTimeout != 0 {
		conn.SetDeadline(time.Now().Add(c.WriteTimeout))
	} else {
		conn.SetDeadline(time.Time{})
	}

	// request write may have been interrupted by server, for example
	// by sending a 4xx in repsonse to a too-long request or a timeout
	requestInterrupted := isClosedError(errors.Cause(requestErr))

	if requestErr == nil || requestInterrupted {
		if statusCode, responseErr = conn.ProxyResponse(w); responseErr == nil {
			if requestInterrupted {
				// suppress the request error in favor of the response data
				requestErr = nil
			}

			// check for hijacking
			if isUpgrade && statusCode == 101 {
				hj, ok := w.(http.Hijacker)
				if !ok {
					panic("rap.Client.ServeHTTP(): http.Hijacker unsupported")
				}
				rwc, buf, err := hj.Hijack()
				if err != nil {
					panic(err)
				}
				defer rwc.Close()
				br := buf.Reader
				if br.Buffered() > 0 {
					panic("rap.Client.ServeHTTP(): websocket client sent data before handshake was complete")
				}
				wg := sync.WaitGroup{}
				wg.Add(1)
				go func() {
					io.Copy(rwc, conn)
					rwc.Close()
					wg.Done()
				}()
				io.Copy(conn, rwc)
				wg.Wait()
			} else {
				// close and write the response body
				// conn.Close()
				_, responseErr = conn.WriteTo(w)
			}
		}
	}

	if responseErr == nil && requestErr == nil {
		return
	}

	panic(fmt.Sprintf("rap.Client.ServeHTTP(): uri=\"%s\"\nrequest error: %+v\nresponse error: %+v\nconn: %+v\n", r.RequestURI, requestErr, responseErr, conn))
}
