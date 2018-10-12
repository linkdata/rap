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

type connControlHandler func(*Conn, FrameData) error

var connControlHandlers = map[ConnControl]connControlHandler{
	ConnControlPanic:       connControlPanicHandler,
	connControlReserved001: connControlReservedHandler,
	ConnControlPing:        connControlPingHandler,
	ConnControlPong:        connControlPongHandler,
	connControlReserved100: connControlReservedHandler,
	connControlReserved101: connControlReservedHandler,
	connControlReserved110: connControlReservedHandler,
	connControlReserved111: connControlReservedHandler,
}

// Conn multiplexes concurrent requests-response Exchanges.
// It maintains the set of ExchangeID's that free and may use them in any order.
type Conn struct {
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

var connNextSerialNumber uint32

func (c *Conn) String() string {
	return fmt.Sprintf("[Conn %x]", c.serialNumber)
}

// NewConn creates a new Conn and initializes it.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	c := &Conn{
		ReadWriteCloser: rwc,
		ReadTimeout:     DefaultReadTimeout,
		WriteTimeout:    DefaultWriteTimeout,
		writeCh:         make(chan FrameData),
		exchanges:       make(chan *Exchange, int(MaxExchangeID)),
		exchangeLookup:  make([]*Exchange, int(MaxExchangeID)+1),
		doneChan:        make(chan struct{}),
		readerWaitGroup: sync.WaitGroup{},
		serialNumber:    atomic.AddUint32(&connNextSerialNumber, 1),
	}

	for idx := range c.exchangeLookup {
		c.exchangeLookup[idx] = NewExchange(c, ExchangeID(idx))
	}
	return c
}

func connControlPingHandler(c *Conn, fd FrameData) (err error) {
	fd.Header().SetConnControl(ConnControlPong)
	select {
	case c.writeCh <- fd:
	case <-c.doneChan:
		return errors.WithStack(serverClosedError{})
	}
	return
}

func connControlPongHandler(c *Conn, fd FrameData) (err error) {
	defer FrameDataFree(fd)
	var now = time.Now().UnixNano()
	atomic.StoreInt64(&c.lastPongRcvd, now)
	if fd.Header().HasPayload() {
		fp := NewFrameParser(fd)
		atomic.StoreInt64(&c.latency, now-fp.ReadInt64())
	}
	return
}

func connControlPanicHandler(c *Conn, fd FrameData) error {
	defer FrameDataFree(fd)
	var msg string
	if fd.Header().HasPayload() {
		fp := NewFrameParser(fd)
		msg, _ = fp.ReadString()
	}
	return errors.Wrap(PanicError{}, msg)
}

func connControlReservedHandler(c *Conn, fd FrameData) error {
	defer FrameDataFree(fd)
	return errors.Wrapf(ProtocolError{}, "unknown conn control frame %v", fd.Header())
}

// Ping sends a ping frame and returns without waiting for response.
func (c *Conn) Ping() {
	fd := FrameDataAlloc()
	fd.WriteConnControl(ConnControlPing)
	atomic.StoreInt64(&c.lastPingSent, time.Now().UnixNano())
	fd.WriteInt64(c.lastPingSent)
	fd.SetSizeValue()
	select {
	case c.writeCh <- fd:
	case <-c.doneChan:
		FrameDataFree(fd)
	}
}

// Latency returns the result of the last successful ping/pong measurement,
// or the zero value if there is no current valid measurement.
func (c *Conn) Latency() (d time.Duration) {
	ping := atomic.LoadInt64(&c.lastPingSent)
	if ping > 0 {
		pong := atomic.LoadInt64(&c.lastPongRcvd)
		if ping <= pong {
			d = time.Nanosecond * time.Duration(pong-ping)
		}
	}
	return
}

// ReadFrom implements io.ReaderFrom.
func (c *Conn) ReadFrom(r io.Reader) (n int64, err error) {
	var unreported int64

	hasCollector := c.StatsCollector != nil

	c.mu.Lock()
	select {
	case <-c.doneChan:
		c.mu.Unlock()
		return
	default:
		c.readerWaitGroup.Add(1)
		defer c.readerWaitGroup.Done()
	}
	c.mu.Unlock()

	for {
		var m int64
		fd := FrameDataAlloc()

		m, err = fd.ReadFrom(r)
		n += m

		if hasCollector {
			unreported += m
			if unreported > int64(FrameMaxSize) {
				c.StatsCollector.AddBytesRead(unreported)
				unreported = 0
			}
		}

		if err != nil {
			// log.Print("Conn.ReadFrom(): fd.ReadFrom(): ", err.Error())
			FrameDataFree(fd)
			break
		}

		if fd.Header().IsConnControl() {
			if err = connControlHandlers[fd.Header().ConnControl()](c, fd); err != nil {
				return
			}
			continue
		}

		e := c.exchangeLookup[fd.Header().ExchangeID()]

		// log.Print("READ ", e, fd)

		if e.starting() {
			if c.ReadTimeout != 0 {
				e.SetReadDeadline(time.Now().Add(c.ReadTimeout))
			} else {
				e.SetReadDeadline(time.Time{})
			}
			if c.WriteTimeout != 0 {
				e.SetWriteDeadline(time.Now().Add(c.WriteTimeout))
			} else {
				e.SetWriteDeadline(time.Time{})
			}
			if c.Handler != nil {
				go e.Serve(c.Handler)
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
func (c *Conn) WriteTo(w io.Writer) (n int64, err error) {
	var unreported int64
	var written int64
	f, hasFlusher := w.(flusher)
	hasCollector := c.StatsCollector != nil

	for err == nil {
		var fd FrameData // fd starts out nil on each iteration

		select {
		case <-c.doneChan:
			return 0, errors.WithStack(serverClosedError{})
		case fd = <-c.writeCh:
		default:
			// no immediately available FrameData, flush the output
			if hasFlusher {
				err = f.Flush()
				if err == nil {
					if hasCollector && unreported > 0 {
						c.StatsCollector.AddBytesWritten(unreported)
						unreported = 0
					}
				}
			}
		}

		if err == nil {
			// do a blocking read if we didn't get a fd
			if fd == nil {
				select {
				case fd = <-c.writeCh:
				case <-c.doneChan:
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
					c.StatsCollector.AddBytesWritten(unreported)
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
func (c *Conn) ServeHTTP(h http.Handler) (err error) {
	c.Handler = h

	errCh := make(chan error, 2)
	defer close(errCh)

	go func() {
		_, err := c.ReadFrom(bufio.NewReaderSize(c.ReadWriteCloser, 64*1024))
		errCh <- err
	}()
	go func() {
		_, err := c.WriteTo(bufio.NewWriterSize(c.ReadWriteCloser, 64*1024))
		errCh <- err
	}()

	err = <-errCh

	if closeErr := c.Close(); closeErr != nil && (err == nil || isClosedError(err)) {
		err = closeErr
	}

	if otherErr := <-errCh; otherErr != nil && err == nil {
		err = otherErr
	}

	return err
}

// returns true if doneChan was closed now, false if already closed
func (c *Conn) closeDoneChanLocked() bool {
	select {
	case <-c.doneChan:
		return false
	default:
		close(c.doneChan)
		return true
	}
}

// Close closes the Conn immediately.
func (c *Conn) Close() (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closeDoneChanLocked() {
		// closing the I/O stream will cause the reader goroutine to stop with an error
		err = c.ReadWriteCloser.Close()

		c.readerWaitGroup.Wait()

		// Drain the unused exchange channel
		for len(c.exchanges) > 0 {
			if e := <-c.exchanges; e != nil {
				e.OnRecycle(nil)
				c.exchangeLookup[e.ID] = nil
			}
		}

		// Close all exchanges we know of (in the lookup table)
		// those will be in-progress exchanges
		for i, e := range c.exchangeLookup {
			if e != nil {
				c.exchangeLookup[i] = nil
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
func (c *Conn) NewExchangeWait(d time.Duration) (e *Exchange) {
	e = c.NewExchange()
	if e == nil {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case e = <-c.exchanges:
		case <-timer.C:
		}
	}
	return
}

// AvailableExchanges returns the number of Exchanges that are
// currently able to be returned from NewExchange(). Note that
// the value may not be exact if there are other goroutines using
// the Conn.
func (c *Conn) AvailableExchanges() (exchangeCount int) {
	exchangeCount = len(c.exchanges)
	lastID := atomic.LoadInt32(&c.exchangeLastID)
	if lastID < int32(MaxExchangeID) {
		exchangeCount += int(int32(MaxExchangeID) - lastID)
	}
	return
}

// NewExchange returns the next available Exchange, or nil if none are available.
func (c *Conn) NewExchange() *Exchange {
	select {
	case e := <-c.exchanges:
		return e
	default:
	}
	for {
		lastID := atomic.LoadInt32(&c.exchangeLastID)
		if lastID >= int32(MaxExchangeID) {
			return nil
		}
		nextID := lastID + 1
		if atomic.CompareAndSwapInt32(&c.exchangeLastID, lastID, nextID) {
			e := NewExchange(c, ExchangeID(nextID))
			e.OnRecycle(c.ExchangeRelease)
			c.exchangeLookup[nextID] = e
			return e
		}
	}
}

// ExchangeWrite allows an Exchange to write a FrameData
func (c *Conn) ExchangeWrite(fd FrameData) error {
	select {
	case c.writeCh <- fd:
		return nil
	case <-c.doneChan:
		return errors.WithStack(serverClosedError{})
	}
}

// ExchangeRelease returns the Exchange to the Conn, allowing it to
// be re-used for other requests.
func (c *Conn) ExchangeRelease(e *Exchange) {
	select {
	case c.exchanges <- e:
	default:
		panic(fmt.Sprint("can't release exchange, len(s.exchanges) ", len(c.exchanges), " cap(s.exchanges) ", cap(c.exchanges), " max ", MaxExchangeID))
	}
	return
}

// ExchangeAbortChannel returns the abort signalling channel
func (c *Conn) ExchangeAbortChannel() <-chan struct{} {
	return c.doneChan
}
