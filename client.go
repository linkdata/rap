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
	"unsafe"

	"github.com/pkg/errors"
)

// Client dials a RAP server, maintaining one or more Muxers.
type Client struct {
	Addr         string        // the address to dial
	DialTimeout  time.Duration // dialing timeout
	ReadTimeout  time.Duration // read timeout (reading the request)
	WriteTimeout time.Duration // write timeout (writing the response)
	mux          *Muxer        // the current active Muxer (atomic access only)
	mu           sync.Mutex    // protects those below
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

// Close closes all Muxers.
func (c *Client) Close() (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, mux := range c.muxers {
		c.muxers[i] = nil
		if mux != nil {
			if muxerr := mux.Close(); err == nil {
				err = muxerr
			}
		}
	}
	c.muxers = nil
	return
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
	go mux.ServeHTTP(nil)
	c.muxers = append(c.muxers, mux)
	return mux
}

// selectBestMux returns an existing Muxer that have free Exchanges,
// or nil if none were found.
// Must run with the mutex locked.
func (c *Client) selectBestMux() (bestMux *Muxer) {
	bestLength := 0
	for _, mux := range c.muxers {
		avail := mux.AvailableExchanges()
		if avail > bestLength {
			bestLength = avail
			bestMux = mux
		}
	}
	return
}

// non-racy "return c.mux"
func (c *Client) getMux() *Muxer {
	return (*Muxer)(atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(&c.mux))))
}

// non-racy "c.mux = mux"
func (c *Client) setMux(mux *Muxer) {
	atomic.StorePointer((*unsafe.Pointer)(unsafe.Pointer(&c.mux)), unsafe.Pointer(mux))
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
	if mux := c.getMux(); mux != nil {
		e = mux.NewExchange()
	}
	if e == nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		bestMux := c.selectBestMux()
		if bestMux != nil {
			if e = bestMux.NewExchange(); e != nil {
				c.setMux(bestMux)
			}
		}
	}
	return
}

// AvailableExchanges returns the number of Exchanges currently not
// serving a request. They may be distributed across many Muxers.
func (c *Client) AvailableExchanges() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.availableExchangesLocked()
}

func (c *Client) availableExchangesLocked() (exchangeCount int) {
	for _, mux := range c.muxers {
		exchangeCount += mux.AvailableExchanges()
	}
	return
}

// NewExchangeMayDial will find a Muxer with free Exchanges or
// create a new one if needed.
func (c *Client) NewExchangeMayDial() (e *Exchange, err error) {
	startTime := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for e == nil {
		bestMux := c.selectBestMux()
		if bestMux == nil {
			// not enough free, dial a new one
			if c.lastAttempt.Before(startTime) {
				bestMux = c.dialLocked()
			}
			if bestMux == nil {
				return nil, c.offlineError()
			}
		}
		// grab an exchange before we publish the new Muxer
		if e = bestMux.NewExchangeWait(c.DialTimeout); e != nil {
			c.setMux(bestMux)
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
	c.serveHTTP(w, r, e)
}

func (c *Client) serveHTTP(w http.ResponseWriter, r *http.Request, e *Exchange) {
	defer e.Close()

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
		e.SetDeadline(time.Now().Add(c.ReadTimeout))
	}

	requestErr = e.WriteRequest(r)

	if c.WriteTimeout != 0 {
		e.SetDeadline(time.Now().Add(c.WriteTimeout))
	} else {
		e.SetDeadline(time.Time{})
	}

	// request write may have been interrupted by server, for example
	// by sending a 4xx in repsonse to a too-long request or a timeout
	requestInterrupted := isClosedError(errors.Cause(requestErr))

	if requestErr == nil || requestInterrupted {
		if statusCode, responseErr = e.ProxyResponse(w); responseErr == nil {
			if requestInterrupted {
				// suppress the request error in favor of the response data
				requestErr = nil
			}

			// write the response body
			if isUpgrade && statusCode == 101 {
				// hijack
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
					io.Copy(rwc, e)
					rwc.Close()
					wg.Done()
				}()
				io.Copy(e, rwc)
				wg.Wait()
			} else {
				_, responseErr = e.WriteTo(w)
			}
		}
	}

	if responseErr == nil && requestErr == nil {
		return
	}

	panic(fmt.Sprintf("rap.Client.ServeHTTP(): uri=\"%s\"\nrequest error: %+v\nresponse error: %+v\nexchange: %+v\n", r.RequestURI, requestErr, responseErr, e))
}
