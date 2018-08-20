package rap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// StatsCollector is the interface required to collect statistics
type StatsCollector interface {
	AddBytesWritten(int64)
	AddBytesRead(int64)
}

// ErrTimeoutReapingExchanges indicates an Exchange leak - one of the connections exchanges
// did not terminate in time.
var ErrTimeoutReapingExchanges = errors.New("timeout reaping exchanges")

// ErrTimeoutWaitingForReader means the reader timed out when closing the connection.
var ErrTimeoutWaitingForReader = errors.New("timeout waiting for reader at close")

// ProtocolError is the error type used for reporting protocol errors,
// all of which are fatal to a connection.
type ProtocolError struct {
	msg string // description of error
}

func (e *ProtocolError) Error() string { return e.msg }

// PanicError is the error type used for reporting peer panic errors,
// all of which are fatal to a connection.
type PanicError struct {
	msg string // description of error
}

func (e *PanicError) Error() string { return e.msg }

type connControlHandler func(*Conn, FrameData) error

var connControlHandlers = map[ConnControl]connControlHandler{
	connControlReserved000: connControlReservedHandler,
	connControlReserved001: connControlReservedHandler,
	ConnControlPing:        connControlPingHandler,
	ConnControlPong:        connControlPongHandler,
	connControlReserved100: connControlReservedHandler,
	connControlReserved101: connControlReservedHandler,
	connControlReserved110: connControlReservedHandler,
	ConnControlPanic:       connControlPanicHandler,
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
	readErrCh          chan error
	writeErrCh         chan error
	mu                 sync.Mutex
	doneChan           chan struct{}
	lastPingSent       int64 // Unix nanoseconds
	lastPongRcvd       int64 // Unix nanoseconds
	latency            int64 // Unix nanoseconds
	isReadClosed       bool
	isWriteClosed      bool
	identity           int64
}

var identityCounter int64

func (c *Conn) String() string {
	return fmt.Sprintf("[Conn %v]", c.identity)
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
		readErrCh:       make(chan error),
		writeErrCh:      make(chan error),
		doneChan:        make(chan struct{}),
		identity:        atomic.AddInt64(&identityCounter, 1),
	}
	return c
}

func connControlPingHandler(c *Conn, fd FrameData) (err error) {
	fd.Header().SetConnControl(ConnControlPong)
	select {
	case c.writeCh <- fd:
	case <-c.doneChan:
		return ErrServerClosed
	}
	return
}

func connControlPongHandler(c *Conn, fd FrameData) (err error) {
	var now = time.Now().UnixNano()
	atomic.StoreInt64(&c.lastPongRcvd, now)
	if fd.Header().HasPayload() {
		fp := NewFrameParser(fd)
		atomic.StoreInt64(&c.latency, now-fp.ReadInt64())
	}
	FrameDataFree(fd)
	return
}

func connControlPanicHandler(c *Conn, fd FrameData) error {
	var msg string
	if fd.Header().HasPayload() {
		fp := NewFrameParser(fd)
		msg, _ = fp.ReadString()
	}
	FrameDataFree(fd)
	return &PanicError{msg: msg}
}

func connControlReservedHandler(c *Conn, fd FrameData) error {
	return &ProtocolError{msg: fmt.Sprintf("unknown conn control frame %v", fd.Header())}
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

	for {
		var m int64
		fd := FrameDataAlloc()

		m, err = fd.ReadFrom(r)
		n += m

		if hasCollector {
			unreported += m
			if unreported > FrameMaxSize {
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

		id := fd.Header().ExchangeID()
		e := c.getExchangeForID(id)
		//	log.Print("READ ", c, fd, " sendW=", e.getSendWindow())

		e.SubmitFrame(fd)
	}
	return
}

func (c *Conn) getExchangeForID(id ExchangeID) (e *Exchange) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e = c.exchangeLookup[id]
	if e == nil {
		e = NewExchange(c, id)
		c.exchangeLookup[id] = e
		if c.Handler != nil {
			go e.RepeatServeHTTP(c.Handler)
		}
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
			return 0, ErrServerClosed
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
					err = ErrServerClosed
				}
				if fd == nil {
					// c.writeCh is closed
					break
				}
			}

			// do the actual write
			// log.Print("WRIT ", c, fd, " sendW=", c.getExchangeForID(fd.Header().ExchangeID()).getSendWindow())
			written, err = fd.WriteTo(w)
			n += written
			FrameDataFree(fd)

			// handle statistics reporting
			if hasCollector {
				unreported += written
				if unreported > FrameMaxSize {
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

	go func() {
		_, err := c.ReadFrom(bufio.NewReaderSize(c.ReadWriteCloser, 64*1024))
		c.readErrCh <- err
		close(c.readErrCh)
	}()
	go func() {
		_, err := c.WriteTo(bufio.NewWriterSize(c.ReadWriteCloser, 64*1024))
		c.writeErrCh <- err
		close(c.writeErrCh)
	}()

	var readErr error
	var writeErr error
	select {
	case readErr = <-c.readErrCh:
		err = readErr
	case writeErr = <-c.writeErrCh:
		err = writeErr
	}

	if closeErr := c.Close(); closeErr != nil && (err == nil || err == io.EOF) {
		err = closeErr
	}

	if readErr == nil {
		if readErr = <-c.readErrCh; err == nil {
			err = readErr
		}
	}

	if writeErr == nil {
		if writeErr = <-c.writeErrCh; err == nil {
			err = writeErr
		}
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

		// Drain the unused exchange channel
	drain:
		for {
			select {
			case e := <-c.exchanges:
				e.OnRecycle(nil)
				c.exchangeLookup[e.ID] = nil
			default:
				break drain
			}
		}

		// Close all exchanges we know of (in the lookup table)
		// those will be in-progress exchanges
		for i, e := range c.exchangeLookup {
			if e != nil {
				c.exchangeLookup[i] = nil
				eerr := e.Close()
				if err == nil {
					switch {
					case eerr == ErrServerClosed:
					case eerr == io.ErrClosedPipe:
					case eerr == io.EOF:
					default:
						err = eerr
					}
				}
			}
		}
	}

	return
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
		return ErrServerClosed
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
