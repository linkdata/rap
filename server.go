package rap

import (
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ErrServerClosed is returned by the Server's Serve method after a call to Close.
var ErrServerClosed = errors.New("rap: Server closed")

// Server listens for incoming RAP connections and creates Conn's for them.
type Server struct {
	Addr           string       // TCP address to listen on, ":10111" if empty
	Handler        http.Handler // HTTP handler to invoke
	MaxConnections int          // maximum number of RAP Conn's to allow
	listeners      map[net.Listener]struct{}
	bytesWritten   int64
	bytesRead      int64
	mu             sync.Mutex
	serveErrorsMu  sync.Mutex
	serveErrors    map[string]int
	connLimiter    chan struct{}
	doneChan       chan struct{}
	activeConn     map[*Conn]struct{}
}

// tcpKeepAliveListener sets TCP keep-alive timeouts on accepted
// connections. It's used by ListenAndServe and ListenAndServeTLS so
// dead TCP connections (e.g. closing laptop mid-download) eventually
// go away.
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}

// Listen announces on the local network address.
func (srv *Server) Listen(address string) (net.Listener, error) {
	ln, err := net.Listen("tcp", address)
	if err == nil {
		srv.Addr = ln.Addr().String()
		ln = tcpKeepAliveListener{ln.(*net.TCPListener)}
	}
	return ln, err
}

// ListenAndServe listens on the TCP network address srv.Addr and then calls
// Serve to handle requests on incoming connections.
// If srv.Addr is blank, ":10111" is used.
func (srv *Server) ListenAndServe() (err error) {
	addr := srv.Addr
	if addr == "" {
		addr = ":10111"
	}
	listener, err := srv.Listen(addr)
	if err == nil {
		err = srv.Serve(listener)
	}
	return
}

// Serve accepts incoming connections on the Listener l, creating a
// new service goroutine for each.  The service goroutines read requests and
// then call srv.Handler to reply to them.
func (srv *Server) Serve(l net.Listener) error {
	defer l.Close()
	var tempDelay time.Duration // how long to sleep on accept failure

	select {
	case <-srv.getDoneChan():
		return ErrServerClosed
	default:
	}

	srv.trackListener(l, true)
	defer srv.trackListener(l, false)

	maxConns := srv.MaxConnections
	if maxConns < 1 {
		maxConns = 1 + (ProtocolMaxConcurrentExchanges / (int(MaxExchangeID) + 1))
	}
	srv.serveErrorsMu.Lock()
	srv.serveErrors = make(map[string]int)
	defer srv.serveErrorsMu.Unlock()
	srv.connLimiter = make(chan struct{}, maxConns)
	for {
		// wait for active connections to fall to allowed levels
		rwc, e := l.Accept()
		if e != nil {
			select {
			case <-srv.getDoneChan():
				return ErrServerClosed
			default:
			}
			if ne, ok := e.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				time.Sleep(tempDelay)
				continue
			}
			return e
		}
		tempDelay = 0
		// Servers are more concerned about throughput than
		// latency so we turn off TCP's NODELAY option.
		if tcpconn, ok := rwc.(*net.TCPConn); ok {
			tcpconn.SetNoDelay(false)
		}
		srv.connLimiter <- struct{}{}
		go func(rwc io.ReadWriteCloser) {
			conn := NewConn(rwc)
			conn.StatsCollector = srv
			srv.trackConn(conn)
			if err := conn.ServeHTTP(srv.Handler); err != nil {
				srv.serveErrorsMu.Lock()
				defer srv.serveErrorsMu.Unlock()
				srv.serveErrors[err.Error()]++
			}
			<-srv.connLimiter
		}(rwc)
	}
}

// ServeErrors returns a copy of the serve errors map
func (srv *Server) ServeErrors() map[string]int {
	srv.serveErrorsMu.Lock()
	defer srv.serveErrorsMu.Unlock()
	m := make(map[string]int)
	for k, v := range srv.serveErrors {
		m[k] = v
	}
	return m
}

func (srv *Server) trackListener(ln net.Listener, add bool) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.listeners == nil {
		srv.listeners = make(map[net.Listener]struct{})
	}
	if add {
		// If the *Server is being reused after a previous
		// Close or Shutdown, reset its doneChan:
		if len(srv.listeners) == 0 && len(srv.activeConn) == 0 {
			srv.doneChan = nil
		}
		srv.listeners[ln] = struct{}{}
	} else {
		delete(srv.listeners, ln)
	}
}

func (srv *Server) trackConn(c *Conn) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.activeConn == nil {
		srv.activeConn = make(map[*Conn]struct{})
	}
	srv.activeConn[c] = struct{}{}
}

// closeIdleConns closes all idle connections and reports whether the
// server is quiescent.
func (srv *Server) closeIdleConns() bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	quiescent := true
	for c := range srv.activeConn {
		c.Close()
		delete(srv.activeConn, c)
	}
	return quiescent
}

func (srv *Server) getDoneChan() <-chan struct{} {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return srv.getDoneChanLocked()
}

func (srv *Server) getDoneChanLocked() chan struct{} {
	if srv.doneChan == nil {
		srv.doneChan = make(chan struct{})
	}
	return srv.doneChan
}

func (srv *Server) closeDoneChanLocked() {
	ch := srv.getDoneChanLocked()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (srv *Server) closeListenersLocked() error {
	var err error
	for ln := range srv.listeners {
		if cerr := ln.Close(); cerr != nil && err == nil {
			err = cerr
		}
		delete(srv.listeners, ln)
	}
	return err
}

// Close immediately closes all active connections.
func (srv *Server) Close() error {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.closeDoneChanLocked()
	err := srv.closeListenersLocked()
	for c := range srv.activeConn {
		c.Close()
		delete(srv.activeConn, c)
	}
	return err
}

// ActiveConns returns the number of active RAP connections.
func (srv *Server) ActiveConns() int {
	return len(srv.connLimiter)
}

// AddBytesWritten adds n to the number of bytes written statistic.
func (srv *Server) AddBytesWritten(n int64) {
	atomic.AddInt64(&srv.bytesWritten, n)
}

// BytesWritten returns the current number of bytes written.
func (srv *Server) BytesWritten() int64 {
	return atomic.LoadInt64(&srv.bytesWritten)
}

// AddBytesRead adds n to the number of bytes read statistic.
func (srv *Server) AddBytesRead(n int64) {
	atomic.AddInt64(&srv.bytesRead, n)
}

// BytesRead returns the current number of bytes read.
func (srv *Server) BytesRead() int64 {
	return atomic.LoadInt64(&srv.bytesRead)
}
