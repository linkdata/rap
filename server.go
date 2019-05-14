// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

package rap

import (
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

type serverClosedError struct{}

func (serverClosedError) Error() string { return "server closed" }

// Server listens for incoming network connections and creates Muxers for them.
type Server struct {
	Addr          string        // TCP address to listen on, ":10111" if empty
	Handler       http.Handler  // HTTP handler to invoke
	MaxMuxers     int           // maximum number of RAP Muxers to allow
	ReadTimeout   time.Duration // read timeout (reading the request)
	WriteTimeout  time.Duration // write timeout (writing the response)
	listeners     map[net.Listener]struct{}
	bytesWritten  int64
	bytesRead     int64
	mu            sync.Mutex
	serveErrorsMu sync.Mutex
	serveErrors   map[string]int
	muxerLimiter  chan struct{}
	doneChan      chan struct{}
	activeMuxer   map[*Muxer]struct{}
	netLog        bool
}

// tcpKeepAliveListener sets TCP keep-alive timeouts on accepted
// network connections. It's used by ListenAndServe and ListenAndServeTLS so
// dead network connections (e.g. closing laptop mid-download) eventually
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

// DefaultListenAddr returns the default address:port
// to listen on.
func (srv *Server) DefaultListenAddr() string {
	return ":10111"
}

func (srv *Server) getListenAddr(addr string) string {
	if addr == "" {
		return srv.DefaultListenAddr()
	}
	return addr
}

// ListenAndServe listens on the TCP network address srv.Addr and then calls
// Serve to handle requests on incoming network connections.
// If srv.Addr is blank, ":10111" is used.
func (srv *Server) ListenAndServe() (err error) {
	listener, err := srv.Listen(srv.getListenAddr(srv.Addr))
	if err == nil {
		err = srv.Serve(listener)
	}
	return
}

// Serve accepts incoming network connections on the Listener l, creating a
// new service goroutine for each.  The service goroutines read requests and
// then call srv.Handler to reply to them.
func (srv *Server) Serve(l net.Listener) error {
	defer l.Close()
	var tempDelay time.Duration // how long to sleep on accept failure

	if err := func() error {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		select {
		case <-srv.getDoneChanLocked():
			return errors.WithStack(serverClosedError{})
		default:
		}
		srv.trackListenerLocked(l, true)
		return nil
	}(); err != nil {
		return err
	}
	defer srv.trackListener(l, false)

	srv.serveErrorsMu.Lock()
	srv.serveErrors = make(map[string]int)
	srv.serveErrorsMu.Unlock()
	for {
		// wait for active network connections to fall to allowed levels
		rwc, err := l.Accept()
		if err != nil {
			select {
			case <-srv.getDoneChan():
				return errors.WithStack(serverClosedError{})
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
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
			return err
		}
		tempDelay = 0
		srv.getMuxerLimiter() <- struct{}{}
		go func(rwc io.ReadWriteCloser) {
			mux := NewMuxer(rwc)
			mux.NetLog(srv.netLog)
			mux.StatsCollector = srv
			mux.ReadTimeout = srv.ReadTimeout
			mux.WriteTimeout = srv.WriteTimeout
			srv.trackMuxer(mux)
			if err := mux.ServeHTTP(srv.Handler); err != nil {
				srv.serveErrorsMu.Lock()
				defer srv.serveErrorsMu.Unlock()
				srv.serveErrors[err.Error()]++
			}
			<-srv.getMuxerLimiter()
		}(rwc)
	}
}

// NetLog enables or disables logging of network data
// and Conn state changes. This is a large volume of
// information, so it's recommended to redirect the
// log output using log.SetOutput().
func (srv *Server) NetLog(state bool) {
	srv.netLog = state
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for mux := range srv.activeMuxer {
		if mux != nil {
			mux.NetLog(state)
		}
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
	srv.trackListenerLocked(ln, add)
}

func (srv *Server) trackListenerLocked(ln net.Listener, add bool) {
	if srv.listeners == nil {
		srv.listeners = make(map[net.Listener]struct{})
	}
	if add {
		// If the *Server is being reused after a previous
		// Close or Shutdown, reset its doneChan:
		if len(srv.listeners) == 0 && len(srv.activeMuxer) == 0 {
			srv.doneChan = nil
		}
		srv.listeners[ln] = struct{}{}
	} else {
		delete(srv.listeners, ln)
	}
}

func (srv *Server) trackMuxer(c *Muxer) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.activeMuxer == nil {
		srv.activeMuxer = make(map[*Muxer]struct{})
	}
	srv.activeMuxer[c] = struct{}{}
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

func (srv *Server) getMuxerLimiter() chan struct{} {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return srv.getMuxerLimiterLocked()
}

func (srv *Server) getMuxerLimiterLocked() chan struct{} {
	if srv.muxerLimiter == nil {
		maxMuxers := srv.MaxMuxers
		if maxMuxers < 1 {
			maxMuxers = 1 + (ProtocolMaxConcurrentConns / (int(MaxConnID) + 1))
		}
		srv.muxerLimiter = make(chan struct{}, maxMuxers)
	}
	return srv.muxerLimiter
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

// Close immediately closes all active network connections and Muxers.
func (srv *Server) Close() error {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.closeDoneChanLocked()
	err := srv.closeListenersLocked()
	for c := range srv.activeMuxer {
		c.Close()
		delete(srv.activeMuxer, c)
	}
	return err
}

// ActiveMuxers returns the number of active RAP muxers.
func (srv *Server) ActiveMuxers() int {
	return len(srv.getMuxerLimiter())
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
