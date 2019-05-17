// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

package rap

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

// StatsCollector is the interface required to collect statistics
type StatsCollector interface {
	AddBytesWritten(int64)
	AddBytesRead(int64)
}

// ErrTimeoutWaitingForReader means the reader timed out when closing the Muxer.
type ErrTimeoutWaitingForReader struct{}

func (ErrTimeoutWaitingForReader) Error() string { return "timeout waiting for reader at close" }

// ProtocolError is the error type used for reporting protocol errors,
// all of which are fatal to a Muxer.
type ProtocolError struct{}

func (err ProtocolError) Error() string { return "protocol error" }

// PanicError is the error type used for reporting peer panic errors,
// all of which are fatal to a muxer.
type PanicError struct{}

func (err PanicError) Error() string { return "peer panic" }

type muxerControlHandler func(*Muxer, FrameData) error

var muxerControlHandlers = map[MuxerControl]muxerControlHandler{
	MuxerControlPanic:       muxerControlPanicHandler,
	muxerControlReserved001: muxerControlReservedHandler,
	MuxerControlPing:        muxerControlPingHandler,
	MuxerControlPong:        muxerControlPongHandler,
	muxerControlReserved100: muxerControlReservedHandler,
	muxerControlReserved101: muxerControlReservedHandler,
	muxerControlReserved110: muxerControlReservedHandler,
	muxerControlReserved111: muxerControlReservedHandler,
}

// Muxer multiplexes concurrent requests-response Conns.
// It maintains the set of ConnID's that free and may use them in any order.
type Muxer struct {
	io.ReadWriteCloser // The I/O endpoint
	StatsCollector     // Where to report statistics (optional)
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	Handler            http.Handler
	writeCh            chan FrameData
	conns              chan *Conn
	connLookup         []*Conn
	connLastID         int32
	mu                 sync.Mutex
	doneChan           chan struct{}
	readerWaitGroup    sync.WaitGroup
	lastPingSent       int64 // Unix nanoseconds
	lastPongRcvd       int64 // Unix nanoseconds
	latency            int64 // Unix nanoseconds
	isReadClosed       bool
	isWriteClosed      bool
	serialNumber       uint32

	muNetLog sync.Mutex
	netLog   bool // if true, log network data using log.Print()
}

var muxerNextSerialNumber uint32

func (mux *Muxer) String() string {
	return fmt.Sprintf("[Muxer %x]", mux.serialNumber)
}

// NewMuxer creates a new Muxer and initializes it.
func NewMuxer(rwc io.ReadWriteCloser) *Muxer {
	mux := &Muxer{
		ReadWriteCloser: rwc,
		ReadTimeout:     DefaultReadTimeout,
		WriteTimeout:    DefaultWriteTimeout,
		writeCh:         make(chan FrameData),
		conns:           make(chan *Conn, int(MaxConnID)),
		connLookup:      make([]*Conn, int(MaxConnID)+1),
		doneChan:        make(chan struct{}),
		readerWaitGroup: sync.WaitGroup{},
		serialNumber:    atomic.AddUint32(&muxerNextSerialNumber, 1),
	}

	for idx := range mux.connLookup {
		conn := NewConn(mux, ConnID(idx))
		conn.netLog = mux.netLog
		mux.connLookup[idx] = conn
	}
	return mux
}

func muxerControlPingHandler(mux *Muxer, fd FrameData) (err error) {
	fd.Header().SetMuxerControl(MuxerControlPong)
	select {
	case mux.writeCh <- fd:
	case <-mux.doneChan:
		return errors.WithStack(serverClosedError{})
	}
	return
}

func muxerControlPongHandler(mux *Muxer, fd FrameData) (err error) {
	defer FrameDataFree(fd)
	var now = time.Now().UnixNano()
	atomic.StoreInt64(&mux.lastPongRcvd, now)
	if fd.Header().HasPayload() {
		fp := NewFrameParser(fd)
		atomic.StoreInt64(&mux.latency, now-fp.ReadInt64())
	}
	return
}

func muxerControlPanicHandler(mux *Muxer, fd FrameData) error {
	defer FrameDataFree(fd)
	var msg string
	if fd.Header().HasPayload() {
		fp := NewFrameParser(fd)
		msg, _ = fp.ReadString()
	}
	return errors.Wrap(PanicError{}, msg)
}

func muxerControlReservedHandler(mux *Muxer, fd FrameData) error {
	defer FrameDataFree(fd)
	return errors.Wrapf(ProtocolError{}, "unknown Muxer control frame %v", fd.Header())
}

// Ping sends a ping frame and returns without waiting for response.
func (mux *Muxer) Ping() {
	fd := FrameDataAlloc()
	fd.WriteMuxerControl(MuxerControlPing)
	atomic.StoreInt64(&mux.lastPingSent, time.Now().UnixNano())
	fd.WriteInt64(mux.lastPingSent)
	fd.SetSizeValue()
	select {
	case mux.writeCh <- fd:
	case <-mux.doneChan:
		FrameDataFree(fd)
	}
}

// Latency returns the result of the last successful ping/pong measurement,
// or the zero value if there is no current valid measurement.
func (mux *Muxer) Latency() (d time.Duration) {
	ping := atomic.LoadInt64(&mux.lastPingSent)
	if ping > 0 {
		pong := atomic.LoadInt64(&mux.lastPongRcvd)
		if ping <= pong {
			d = time.Nanosecond * time.Duration(pong-ping)
		}
	}
	return
}

// NetLog enables or disables logging of network data
// and Conn state changes.
func (mux *Muxer) NetLog(state bool) {
	mux.mu.Lock()
	defer mux.mu.Unlock()
	mux.netLog = state
	for _, conn := range mux.connLookup {
		conn.netLog = state
	}
}

// ReadFrom implements io.ReaderFrom.
func (mux *Muxer) ReadFrom(r io.Reader) (n int64, err error) {
	var unreported int64

	hasCollector := mux.StatsCollector != nil

	mux.mu.Lock()
	select {
	case <-mux.doneChan:
		mux.mu.Unlock()
		return
	default:
		mux.readerWaitGroup.Add(1)
		defer mux.readerWaitGroup.Done()
	}
	mux.mu.Unlock()

	for {
		var m int64
		fd := FrameDataAlloc()

		m, err = fd.ReadFrom(r)
		n += m

		readTimedOut := (m == 0)

		if hasCollector {
			unreported += m
			if unreported > 0 {
				if readTimedOut || unreported > int64(FrameMaxSize) {
					mux.StatsCollector.AddBytesRead(unreported)
					unreported = 0
				}
			}
		}

		// if read timed out, check if we are closing
		if readTimedOut && mux.isClosed() {
			FrameDataFree(fd)
			break
		}

		if err != nil {
			// log.Print("Muxer.ReadFrom(): fd.ReadFrom(): ", err.Error())
			FrameDataFree(fd)
			break
		}

		if fd.Header().IsMuxerControl() {
			if err = muxerControlHandlers[fd.Header().MuxerControl()](mux, fd); err != nil {
				return
			}
			continue
		}

		conn := mux.connLookup[fd.Header().ConnID()]

		if mux.netLog {
			mux.muNetLog.Lock()
			log.Print("READ ", conn, fd)
			mux.muNetLog.Unlock()
		}

		if conn.starting() {
			if mux.ReadTimeout != 0 {
				conn.SetReadDeadline(time.Now().Add(mux.ReadTimeout))
			} else {
				conn.SetReadDeadline(time.Time{})
			}
			if mux.WriteTimeout != 0 {
				conn.SetWriteDeadline(time.Now().Add(mux.WriteTimeout))
			} else {
				conn.SetWriteDeadline(time.Time{})
			}
			if mux.Handler != nil {
				go conn.Serve(mux.Handler)
			}
		}

		conn.SubmitFrame(fd)
	}
	return
}

type flusher interface {
	Flush() error
}

// WriteTo implements io.WriterTo. FrameData arriving on the write channel
// are buffered and written to w until the write channel is closed or
// an error occurs.
func (mux *Muxer) WriteTo(w io.Writer) (n int64, err error) {
	var unreported int64
	var written int64
	f, hasFlusher := w.(flusher)
	hasCollector := mux.StatsCollector != nil

	for err == nil {
		var fd FrameData // fd starts out nil on each iteration

		select {
		case <-mux.doneChan:
			return 0, errors.WithStack(serverClosedError{})
		case fd = <-mux.writeCh:
		default:
			// no immediately available FrameData, flush the output
			if hasFlusher {
				err = f.Flush()
				if err == nil {
					if hasCollector && unreported > 0 {
						mux.StatsCollector.AddBytesWritten(unreported)
						unreported = 0
					}
				}
			}
		}

		if err == nil {
			// do a blocking read if we didn't get a fd
			if fd == nil {
				select {
				case fd = <-mux.writeCh:
				case <-mux.doneChan:
					err = errors.WithStack(serverClosedError{})
				}
				if fd == nil {
					// c.writeCh is closed
					break
				}
			}

			// do the actual write
			if mux.netLog {
				mux.muNetLog.Lock()
				log.Print("WRIT ", mux.getConn(fd.Header().ConnID()), fd)
				mux.muNetLog.Unlock()
			}

			written, err = fd.WriteTo(w)
			n += written
			FrameDataFree(fd)

			// handle statistics reporting
			if hasCollector {
				unreported += written
				if unreported > int64(FrameMaxSize) {
					mux.StatsCollector.AddBytesWritten(unreported)
					unreported = 0
				}
			}
		}
	}

	if hasFlusher {
		if flusherr := f.Flush(); err == nil {
			err = flusherr
		}
	}

	return
}

func (mux *Muxer) getConn(connID ConnID) *Conn {
	if connID > MaxConnID {
		return nil
	}
	return mux.connLookup[connID]
}

func (mux *Muxer) isClosed() bool {
	select {
	case <-mux.doneChan:
		return true
	default:
		return false
	}
}

// ServeHTTP processes incoming and outgoing frames for the Muxer until closed.
func (mux *Muxer) ServeHTTP(h http.Handler) (err error) {
	mux.Handler = h

	errCh := make(chan error, 2)
	defer close(errCh)

	go func() {
		_, err := mux.ReadFrom(bufio.NewReaderSize(mux.ReadWriteCloser, 64*1024))
		errCh <- err
	}()
	go func() {
		_, err := mux.WriteTo(bufio.NewWriterSize(mux.ReadWriteCloser, 64*1024))
		errCh <- err
	}()

	err = <-errCh

	if !mux.isClosed() {
		if closeErr := mux.Close(); closeErr != nil && (err == nil || isClosedError(err)) {
			err = closeErr
		}
	}

	if otherErr := <-errCh; otherErr != nil && err == nil {
		err = otherErr
	}

	return err
}

// returns true if doneChan was closed now, false if already closed
func (mux *Muxer) closeDoneChanLocked() bool {
	select {
	case <-mux.doneChan:
		return false
	default:
		close(mux.doneChan)
		return true
	}
}

// returns true if doneChan was closed now, false if already closed
func (mux *Muxer) closeDoneChan() bool {
	mux.mu.Lock()
	defer mux.mu.Unlock()
	return mux.closeDoneChanLocked()
}

func (mux *Muxer) closeReaderLocked() (err error) {
	// closing the I/O stream will cause the reader goroutine to stop with an error
	err = mux.ReadWriteCloser.Close()
	mux.readerWaitGroup.Wait()
	return
}

func (mux *Muxer) closeReader() (err error) {
	mux.mu.Lock()
	defer mux.mu.Unlock()
	return mux.closeReaderLocked()
}

// returns true if there are no more active conns
func (mux *Muxer) closeIdleConnsLocked() bool {
	for len(mux.conns) > 0 {
		if conn := <-mux.conns; conn != nil {
			conn.OnRecycle(nil)
			mux.connLookup[conn.ID] = nil
		}
	}
	for _, conn := range mux.connLookup {
		if conn != nil {
			return false
		}
	}
	return true
}

// returns true if there are no more active conns
func (mux *Muxer) closeIdleConns() bool {
	mux.mu.Lock()
	defer mux.mu.Unlock()
	return mux.closeIdleConnsLocked()
}

func (mux *Muxer) closeActiveConnsLocked() (err error) {
	// Close all Conns we know of (in the lookup table)
	// those will be in-progress Conns
	for i, conn := range mux.connLookup {
		if conn != nil {
			mux.connLookup[i] = nil
			eerr := conn.Close()
			if err == nil && !isClosedError(eerr) {
				err = eerr
			}
		}
	}
	return
}

// Close closes the Muxer immediately.
func (mux *Muxer) Close() (err error) {
	mux.mu.Lock()
	defer mux.mu.Unlock()

	if mux.closeDoneChanLocked() {
		err = mux.closeReaderLocked()
		mux.closeIdleConnsLocked()
		if err2 := mux.closeActiveConnsLocked(); err2 != nil && err == nil {
			err = err2
		}
	}
	return
}

// Shutdown attempts a graceful shutdown of the muxer.
// It waits for active requests to finish,
// then aborts those that exceed the read time out.
func (mux *Muxer) Shutdown() error {
	if mux.closeDoneChan() {
		defer mux.closeReader()
	}

	ticker := time.NewTicker(mux.ReadTimeout)
	defer ticker.Stop()
	for {
		if mux.closeIdleConns() {
			return nil
		}
		select {
		case <-ticker.C:
			mux.Close()
			return timeoutError{}
		default:
		}
	}
}

func isClosedError(err error) bool {
	switch errors.Cause(err) {
	case serverClosedError{}:
		return true
	case io.ErrClosedPipe:
		return true
	case io.EOF:
		return true
	}
	return false
}

// NewConnWait returns the next available Conn, or nil if timed out.
func (mux *Muxer) NewConnWait(d time.Duration) (conn *Conn) {
	conn = mux.NewConn()
	if conn == nil {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case conn = <-mux.conns:
		case <-timer.C:
		}
	}
	if conn != nil {
		conn.netLog = mux.netLog
	}
	return
}

// AvailableConns returns the number of Conns that are
// currently able to be returned from NewConn(). Note that
// the value may not be exact if there are other goroutines using
// the Muxer.
func (mux *Muxer) AvailableConns() (connCount int) {
	connCount = len(mux.conns)
	lastID := atomic.LoadInt32(&mux.connLastID)
	if lastID < int32(MaxConnID) {
		connCount += int(int32(MaxConnID) - lastID)
	}
	return
}

// NewConn returns the next available Conn, or nil if none are available.
func (mux *Muxer) NewConn() (conn *Conn) {
	select {
	case conn = <-mux.conns:
		conn.netLog = mux.netLog
		return
	default:
	}
	for {
		lastID := atomic.LoadInt32(&mux.connLastID)
		if lastID >= int32(MaxConnID) {
			return
		}
		nextID := lastID + 1
		if atomic.CompareAndSwapInt32(&mux.connLastID, lastID, nextID) {
			conn = NewConn(mux, ConnID(nextID))
			conn.netLog = mux.netLog
			conn.OnRecycle(mux.ConnRelease)
			mux.connLookup[nextID] = conn
			return
		}
	}
}

// ConnWrite allows an Conn to write a FrameData
func (mux *Muxer) ConnWrite(fd FrameData) error {
	select {
	case mux.writeCh <- fd:
		return nil
	case <-mux.doneChan:
		return errors.WithStack(serverClosedError{})
	}
}

// ConnRelease returns the Conn to the Muxer, allowing it to
// be re-used for other requests.
func (mux *Muxer) ConnRelease(conn *Conn) {
	select {
	case mux.conns <- conn:
		if mux.netLog {
			log.Print("IDLE ", conn)
		}
	default:
		panic(fmt.Sprint("can't release Conn, len(s.conns) ", len(mux.conns), " cap(s.conns) ", cap(mux.conns), " max ", MaxConnID, " conn ", conn))
	}
	return
}

// ConnAbortChannel returns the abort signalling channel
func (mux *Muxer) ConnAbortChannel() <-chan struct{} {
	return mux.doneChan
}
