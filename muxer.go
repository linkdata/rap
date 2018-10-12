package rap

import (
	"bufio"
	"fmt"
	"io"
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

// ErrTimeoutWaitingForReader means the reader timed out when closing the connection.
type ErrTimeoutWaitingForReader struct{}

func (ErrTimeoutWaitingForReader) Error() string { return "timeout waiting for reader at close" }

// ProtocolError is the error type used for reporting protocol errors,
// all of which are fatal to a connection.
type ProtocolError struct{}

func (e ProtocolError) Error() string { return "protocol error" }

// PanicError is the error type used for reporting peer panic errors,
// all of which are fatal to a connection.
type PanicError struct{}

func (e PanicError) Error() string { return "peer panic" }

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

// Muxer multiplexes concurrent requests-response Exchanges.
// It maintains the set of ExchangeID's that free and may use them in any order.
type Muxer struct {
	io.ReadWriteCloser // The I/O endpoint, usually a TCP connection
	StatsCollector     // Where to report statistics (optional)
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	Handler            http.Handler
	writeCh            chan FrameData
	exchanges          chan *Exchange
	exchangeLookup     []*Exchange
	exchangeLastID     int32
	mu                 sync.Mutex
	doneChan           chan struct{}
	readerWaitGroup    sync.WaitGroup
	lastPingSent       int64 // Unix nanoseconds
	lastPongRcvd       int64 // Unix nanoseconds
	latency            int64 // Unix nanoseconds
	isReadClosed       bool
	isWriteClosed      bool
	serialNumber       uint32
}

var muxerNextSerialNumber uint32

func (mux *Muxer) String() string {
	return fmt.Sprintf("[Conn %x]", mux.serialNumber)
}

// NewMuxer creates a new Muxer and initializes it.
func NewMuxer(rwc io.ReadWriteCloser) *Muxer {
	mux := &Muxer{
		ReadWriteCloser: rwc,
		ReadTimeout:     DefaultReadTimeout,
		WriteTimeout:    DefaultWriteTimeout,
		writeCh:         make(chan FrameData),
		exchanges:       make(chan *Exchange, int(MaxExchangeID)),
		exchangeLookup:  make([]*Exchange, int(MaxExchangeID)+1),
		doneChan:        make(chan struct{}),
		readerWaitGroup: sync.WaitGroup{},
		serialNumber:    atomic.AddUint32(&muxerNextSerialNumber, 1),
	}

	for idx := range mux.exchangeLookup {
		mux.exchangeLookup[idx] = NewExchange(mux, ExchangeID(idx))
	}
	return mux
}

func muxerControlPingHandler(mux *Muxer, fd FrameData) (err error) {
	fd.Header().SetConnControl(MuxerControlPong)
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
	return errors.Wrapf(ProtocolError{}, "unknown conn control frame %v", fd.Header())
}

// Ping sends a ping frame and returns without waiting for response.
func (mux *Muxer) Ping() {
	fd := FrameDataAlloc()
	fd.WriteConnControl(MuxerControlPing)
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

		if hasCollector {
			unreported += m
			if unreported > int64(FrameMaxSize) {
				mux.StatsCollector.AddBytesRead(unreported)
				unreported = 0
			}
		}

		if err != nil {
			// log.Print("Conn.ReadFrom(): fd.ReadFrom(): ", err.Error())
			FrameDataFree(fd)
			break
		}

		if fd.Header().IsConnControl() {
			if err = muxerControlHandlers[fd.Header().ConnControl()](mux, fd); err != nil {
				return
			}
			continue
		}

		e := mux.exchangeLookup[fd.Header().ExchangeID()]

		// log.Print("READ ", e, fd)

		if e.starting() {
			if mux.ReadTimeout != 0 {
				e.SetReadDeadline(time.Now().Add(mux.ReadTimeout))
			} else {
				e.SetReadDeadline(time.Time{})
			}
			if mux.WriteTimeout != 0 {
				e.SetWriteDeadline(time.Now().Add(mux.WriteTimeout))
			} else {
				e.SetWriteDeadline(time.Time{})
			}
			if mux.Handler != nil {
				go e.Serve(mux.Handler)
			}
		}

		e.SubmitFrame(fd)
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
			// log.Print("WRIT ", c.getExchangeForID(fd.Header().ExchangeID()), fd)
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

// ServeHTTP processes incoming and outgoing frames for the Conn until closed.
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

	if closeErr := mux.Close(); closeErr != nil && (err == nil || isClosedError(err)) {
		err = closeErr
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

// Close closes the Conn immediately.
func (mux *Muxer) Close() (err error) {
	mux.mu.Lock()
	defer mux.mu.Unlock()

	if mux.closeDoneChanLocked() {
		// closing the I/O stream will cause the reader goroutine to stop with an error
		err = mux.ReadWriteCloser.Close()

		mux.readerWaitGroup.Wait()

		// Drain the unused exchange channel
		for len(mux.exchanges) > 0 {
			if e := <-mux.exchanges; e != nil {
				e.OnRecycle(nil)
				mux.exchangeLookup[e.ID] = nil
			}
		}

		// Close all exchanges we know of (in the lookup table)
		// those will be in-progress exchanges
		for i, e := range mux.exchangeLookup {
			if e != nil {
				mux.exchangeLookup[i] = nil
				eerr := e.Close()
				if err == nil && !isClosedError(eerr) {
					err = eerr
				}
			}
		}
	}

	return
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

// NewExchangeWait returns the next available Exchange, or nil if timed out.
func (mux *Muxer) NewExchangeWait(d time.Duration) (e *Exchange) {
	e = mux.NewExchange()
	if e == nil {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case e = <-mux.exchanges:
		case <-timer.C:
		}
	}
	return
}

// AvailableExchanges returns the number of Exchanges that are
// currently able to be returned from NewExchange(). Note that
// the value may not be exact if there are other goroutines using
// the Conn.
func (mux *Muxer) AvailableExchanges() (exchangeCount int) {
	exchangeCount = len(mux.exchanges)
	lastID := atomic.LoadInt32(&mux.exchangeLastID)
	if lastID < int32(MaxExchangeID) {
		exchangeCount += int(int32(MaxExchangeID) - lastID)
	}
	return
}

// NewExchange returns the next available Exchange, or nil if none are available.
func (mux *Muxer) NewExchange() *Exchange {
	select {
	case e := <-mux.exchanges:
		return e
	default:
	}
	for {
		lastID := atomic.LoadInt32(&mux.exchangeLastID)
		if lastID >= int32(MaxExchangeID) {
			return nil
		}
		nextID := lastID + 1
		if atomic.CompareAndSwapInt32(&mux.exchangeLastID, lastID, nextID) {
			e := NewExchange(mux, ExchangeID(nextID))
			e.OnRecycle(mux.ExchangeRelease)
			mux.exchangeLookup[nextID] = e
			return e
		}
	}
}

// ExchangeWrite allows an Exchange to write a FrameData
func (mux *Muxer) ExchangeWrite(fd FrameData) error {
	select {
	case mux.writeCh <- fd:
		return nil
	case <-mux.doneChan:
		return errors.WithStack(serverClosedError{})
	}
}

// ExchangeRelease returns the Exchange to the Conn, allowing it to
// be re-used for other requests.
func (mux *Muxer) ExchangeRelease(e *Exchange) {
	select {
	case mux.exchanges <- e:
	default:
		panic(fmt.Sprint("can't release exchange, len(s.exchanges) ", len(mux.exchanges), " cap(s.exchanges) ", cap(mux.exchanges), " max ", MaxExchangeID))
	}
	return
}

// ExchangeAbortChannel returns the abort signalling channel
func (mux *Muxer) ExchangeAbortChannel() <-chan struct{} {
	return mux.doneChan
}
