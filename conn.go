package rap

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// StatsCollector is the interface required to collect statistics
type StatsCollector interface {
	AddBytesWritten(int64)
	AddBytesRead(int64)
}

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
	writeCh            chan FrameData
	readChs            []chan FrameData
	exchanges          chan *Exchange
	exchangeLookup     []*Exchange
	readErrCh          chan error
	writeErrCh         chan error
	lastPingSent       int64 // Unix nanoseconds
	lastPongRcvd       int64 // Unix nanoseconds
	latency            int64 // Unix nanoseconds
}

func (c *Conn) String() string {
	return fmt.Sprintf("[Conn]")
}

// NewConn creates a new Conn and initializes it.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	c := &Conn{
		ReadWriteCloser: rwc,
		ReadTimeout:     DefaultReadTimeout,
		WriteTimeout:    DefaultWriteTimeout,
		writeCh:         make(chan FrameData),
		readChs:         make([]chan FrameData, int(MaxExchangeID)+1),
		exchanges:       make(chan *Exchange, int(MaxExchangeID)+1),
		exchangeLookup:  make([]*Exchange, int(MaxExchangeID)+1),
		readErrCh:       make(chan error),
		writeErrCh:      make(chan error),
	}
	for i := range c.exchangeLookup {
		c.readChs[i] = make(chan FrameData, MaxSendWindowSize)
		e := NewExchange(c, ExchangeID(i))
		c.exchangeLookup[i] = e
		c.exchanges <- e
	}
	return c
}

func connControlPingHandler(c *Conn, fd FrameData) (err error) {
	fd.Header().SetConnControl(ConnControlPong)
	c.writeCh <- fd
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
	c.writeCh <- fd
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

		// log.Print("Conn.ReadFrom(): ", fd)

		if fd.Header().IsConnControl() {
			if err = connControlHandlers[fd.Header().ConnControl()](c, fd); err != nil {
				return
			}
			continue
		}

		e := c.exchangeLookup[fd.Header().ExchangeID()]
		// log.Print("Conn.ReadFrom(): ", fd, " sendW=", atomic.LoadInt32(&e.sendWindow), " recvW=", atomic.LoadInt32(&e.recvWindow))

		if fd.Header().HasPayload() {
			c.readChs[fd.Header().ExchangeID()] <- fd
		} else {
			FrameDataFree(fd)
			e.ackCh <- struct{}{}
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
				if fd = <-c.writeCh; fd == nil {
					// c.writeCh is closed
					break
				}
			}

			// do the actual write
			// log.Print("Conn.WriteTo(): ", fd)
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
	if h != nil {
	drainExchanges:
		for {
			select {
			case e := <-c.exchanges:
				go e.ServeHTTP(h)
			default:
				break drainExchanges
			}
		}
	}

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

	if closeErr := c.Close(); err == nil {
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

// CloseRead closes the reading part of a Conn
func (c *Conn) CloseRead() (err error) {
	select {
	case err = <-c.readErrCh:
		// reader already done
	default:
		timer := time.NewTimer(c.ReadTimeout)
		defer timer.Stop()
		// closing the I/O stream will cause the reader to stop with an error
		if err = c.ReadWriteCloser.Close(); err != nil {
			// log.Print("Conn.CloseRead(): c.ReadWriteCloser.Close(): ", err.Error())
			return
		}
		select {
		case err = <-c.readErrCh:
		case <-timer.C:
			log.Print("Conn.CloseRead(): timeout waiting for reader")
			err = io.ErrUnexpectedEOF
			return
		}
	}
	// close exchanges' readCh as we will no longer write to them
	for i, e := range c.exchangeLookup {
		if e != nil {
			close(e.ackCh)
		}
		close(c.readChs[i])
	}
	return
}

// CloseWrite stops the writing part of a Conn
func (c *Conn) CloseWrite() (err error) {
	reapCount := 0
	timer := time.NewTimer(c.ReadTimeout)
	defer timer.Stop()
	for reapCount < cap(c.exchanges) {
		select {
		case fd := <-c.writeCh:
			FrameDataFree(fd)
		case <-c.exchanges:
			reapCount++
		case <-timer.C:
			log.Print("Conn.Close(): timeout reaping exchanges (", cap(c.exchanges)-reapCount, " leaked)")
			return io.ErrShortWrite
		}
	}
	close(c.writeCh)
	return
}

// Close closes the Conn
func (c *Conn) Close() (err error) {
	err = c.CloseRead()
	if err2 := c.CloseWrite(); err == nil {
		err = err2
	}
	return
}

// NewExchangeWait returns the next available Exchange, or nil if timed out.
func (c *Conn) NewExchangeWait(d time.Duration) *Exchange {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case e := <-c.exchanges:
		return e
	case <-timer.C:
		return nil
	}
}

// NewExchange returns the next available Exchange, or nil if none are available.
func (c *Conn) NewExchange() *Exchange {
	select {
	case e := <-c.exchanges:
		return e
	default:
		return nil
	}
}

// ExchangeWrite is called by an Exchange when it wants to write
// a FrameData.
func (c *Conn) ExchangeWrite(exchangeID ExchangeID, fd FrameData) error {
	c.writeCh <- fd
	return nil
}

// ExchangeRead returns a FrameData for an Exchange read.
func (c *Conn) ExchangeRead(exchangeID ExchangeID) (FrameData, error) {
	return <-c.readChs[exchangeID], nil
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

// ExchangeTimeout returns the Exchange timeout duration
func (c *Conn) ExchangeTimeout() time.Duration {
	return c.WriteTimeout
}
