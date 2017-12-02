package rap

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// StatsCollector is the interface required to collect statistics
type StatsCollector interface {
	AddBytesWritten(int64)
	AddBytesRead(int64)
}

type connControlHandler func(*Conn, FrameData) error

var connControlHandlers = map[ConnControl]connControlHandler{
	ConnControlPing:      connControlPingHandler,
	ConnControlSetup:     connControlSetupHandler,
	ConnControlStopping:  connControlStoppingHandler,
	ConnControlStopped:   connControlStoppedHandler,
	ConnControlPong:      connControlPongHandler,
	connControlReserved1: connControlReservedHandler,
	connControlReserved2: connControlReservedHandler,
	connControlReserved3: connControlReservedHandler,
}

// Conn multiplexes concurrent requests-response Exchanges.
// It maintains the set of ExchangeID's that free and may use them in any order.
type Conn struct {
	io.ReadWriteCloser // The I/O endpoint, usually a TCP connection
	StatsCollector     // Where to report statistics (optional)
	writeCh            chan FrameData
	exchanges          chan *Exchange
	exchangeLookup     []*Exchange
	readErrCh          chan error
	writeErrCh         chan error
}

func (c *Conn) String() string {
	return fmt.Sprintf("[Conn]")
}

// NewConn creates a new Conn and initializes it.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	c := &Conn{
		ReadWriteCloser: rwc,
		writeCh:         make(chan FrameData),
		exchanges:       make(chan *Exchange, int(MaxExchangeID)+1),
		exchangeLookup:  make([]*Exchange, int(MaxExchangeID)+1),
		readErrCh:       make(chan error),
		writeErrCh:      make(chan error),
	}
	for i := range c.exchangeLookup {
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

func connControlSetupHandler(c *Conn, fd FrameData) (err error) {
	FrameDataFree(fd)
	return
}

func connControlStoppingHandler(c *Conn, fd FrameData) (err error) {
	FrameDataFree(fd)
	return
}

func connControlStoppedHandler(c *Conn, fd FrameData) (err error) {
	FrameDataFree(fd)
	return
}

func connControlPongHandler(c *Conn, fd FrameData) (err error) {
	FrameDataFree(fd)
	return
}

func connControlReservedHandler(c *Conn, fd FrameData) (err error) {
	err = fmt.Errorf("unknown conn control frame %v", fd.Header())
	FrameDataFree(fd)
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
				log.Fatal("Conn.ReadFrom(): connControlHandler ", fd.Header().ConnControl(), ": ", err.Error())
				break
			}
			continue
		}

		e := c.exchangeLookup[fd.Header().ExchangeID()]
		// log.Print("Conn.ReadFrom(): ", fd, " sendW=", atomic.LoadInt32(&e.sendWindow), " recvW=", atomic.LoadInt32(&e.recvWindow))

		if fd.Header().HasPayload() {
			e.readCh <- fd
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
	f, hasFlusher := w.(flusher)
	hasCollector := c.StatsCollector != nil

	for {
		var fd FrameData // fd starts out nil on each iteration

		select {
		case fd = <-c.writeCh:
		default:
			// no immediately available FrameData.
			// Flush the output and do a blocking read.
			if hasFlusher {
				err = f.Flush()
				if err != nil {
					break
				}
				if hasCollector && unreported > 0 {
					c.StatsCollector.AddBytesWritten(unreported)
					unreported = 0
				}
			}
			fd = <-c.writeCh
		}

		if fd == nil {
			break
		}

		// do the actual write
		// log.Print("Conn.WriteTo(): ", fd)
		var written int64
		written, err = fd.WriteTo(w)
		FrameDataFree(fd)
		n += written

		// handle statistics reporting
		if hasCollector {
			unreported += written
			if unreported > FrameMaxSize {
				c.StatsCollector.AddBytesWritten(unreported)
				unreported = 0
			}
		}

		if err != nil {
			break
		}
	}

	if hasFlusher {
		if flusherr := f.Flush(); err == nil {
			err = flusherr
		}
	}

	return
}

// Serve processes incoming and outgoing frames for the Conn until closed.
func (c *Conn) Serve(h http.Handler) (err error) {
	if h != nil {
	drainExchanges:
		for {
			select {
			case e := <-c.exchanges:
				go e.serve(h)
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
	case writeErr = <-c.writeErrCh:
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
		timer := time.NewTimer(ReadTimeout)
		defer timer.Stop()
		// closing the I/O stream will cause the reader to stop with an error
		if err = c.ReadWriteCloser.Close(); err != nil {
			log.Print("Conn.CloseRead(): c.ReadWriteCloser.Close(): ", err.Error())
			return
		}
		select {
		case err = <-c.readErrCh:
		case <-timer.C:
			log.Print("Conn.CloseRead(): timeout waiting for reader")
			err = io.ErrNoProgress
			return
		}
	}
	// close exchanges' readCh as we will no longer write to them
	for _, e := range c.exchangeLookup {
		if e != nil {
			close(e.readCh)
			close(e.ackCh)
		}
	}
	return
}

// CloseWrite stops the writing part of a Conn
func (c *Conn) CloseWrite() (err error) {
	reapCount := 0
	timer := time.NewTimer(ReadTimeout)
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

// ExchangeWriteChannel returns the FrameData channel that an Exchange should
// use when producing output frames. Called once when Exchange is initialized.
func (c *Conn) ExchangeWriteChannel() chan FrameData {
	return c.writeCh
}

// ExchangeReadChannel returns the FrameData channel that an Exchange should
// use when reading input frames. Called once when Exchange is initialized.
func (c *Conn) ExchangeReadChannel() chan FrameData {
	return make(chan FrameData, MaxSendWindowSize)
}

// ExchangeRelease returns the Exchange to the Conn, allowing it to
// be re-used for other requests.
func (c *Conn) ExchangeRelease(e *Exchange) {
	select {
	case c.exchanges <- e:
	default:
		log.Print("can't release exchange, len(s.exchanges) ", len(c.exchanges), " cap(s.exchanges) ", cap(c.exchanges), " max ", MaxExchangeID)
	}
	return
}

// ExchangeWriteTimeout returns the Exchange write timeout, in seconds
func (c *Conn) ExchangeWriteTimeout() time.Duration {
	return WriteTimeout
}

// ExchangeReadTimeout returns the Exchange read timeout
func (c *Conn) ExchangeReadTimeout() time.Duration {
	return ReadTimeout
}
