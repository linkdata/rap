// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

package rap

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

// ConnID identifies a in-progress request/response.
type ConnID uint16

func (connID ConnID) String() string {
	return fmt.Sprintf("[ID %04x]", uint16(connID))
}

// ErrUnhandledRecordType is returned when a frame head record type is unknown or unexpected.
type ErrUnhandledRecordType struct {
	Value RecordType // The invalid record type value received.
}

func (e ErrUnhandledRecordType) Error() string {
	return fmt.Sprintf("unhandled record type 0x%02x", byte(e.Value))
}

// ErrMissingFrameHead is returned when an RAP frame was expected to have the HEAD bit set and contain a RAP record.
type ErrMissingFrameHead struct{}

func (ErrMissingFrameHead) Error() string { return "missing frame head" }

// ConnMuxer is the interface that a Conn needs in order to
// communicate with the outside world and clean up.
type ConnMuxer interface {
	// ConnWrite allows a Conn to write a FrameData
	ConnWrite(fd FrameData) error
	// ConnAbortChannel returns the channel that is closed when owner is closing
	ConnAbortChannel() <-chan struct{}
}

// connDeadline is an abstraction for handling timeouts.
type connDeadline struct {
	mu     sync.Mutex // Guards timer and cancel
	timer  *time.Timer
	cancel chan struct{} // Must be non-nil
}

func makeConnDeadline() connDeadline {
	return connDeadline{cancel: make(chan struct{})}
}

// set sets the point in time when the deadline will time out.
// A timeout event is signaled by closing the channel returned by waiter.
// Once a timeout has occurred, the deadline can be refreshed by specifying a
// t value in the future.
//
// A zero value for t prevents timeout.
func (d *connDeadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil && !d.timer.Stop() {
		<-d.cancel // Wait for the timer callback to finish and close cancel
	}
	d.timer = nil

	// Time is zero, then there is no deadline.
	closed := isClosedChan(d.cancel)
	if t.IsZero() {
		if closed {
			d.cancel = make(chan struct{})
		}
		return
	}

	// Time in the future, setup a timer to cancel in the future.
	if dur := time.Until(t); dur > 0 {
		if closed {
			d.cancel = make(chan struct{})
		}
		d.timer = time.AfterFunc(dur, func() {
			close(d.cancel)
		})
		return
	}

	// Time in the past, so close immediately.
	if !closed {
		close(d.cancel)
	}
}

// wait returns a channel that is closed when the deadline is exceeded.
func (d *connDeadline) wait() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel
}

func isClosedChan(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "deadline exceeded" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type runState int32

const (
	runStateIdle         = runState(0)
	runStateActive       = runState(1)
	runStateRemoteClosed = runState(2)
	runStateLocalWaitAck = runState(3)
	runStateLocalClosed  = runState(4)
	runStateWaitAck      = runState(5)
	runStateRecycle      = runState(6)
)

var runStateTexts = map[runState]string{
	runStateIdle:         "IDLE  ",
	runStateActive:       " ACTIV",
	runStateRemoteClosed: "  RCL ",
	runStateLocalWaitAck: "  LWA ",
	runStateLocalClosed:  "   LCL",
	runStateWaitAck:      "   WA ",
	runStateRecycle:      "    RE",
}

func getRunStateText(rs runState) string {
	if rs < runStateIdle || rs > runStateRecycle {
		return strconv.FormatInt(int64(rs), 10)
	}
	return runStateTexts[rs]
}

// Conn is essentially a pipe. It maintains the state of a request-response
// or WebSocket connection, moves data between the RAP and HTTP connections and
// handles the flow control mechanism, which is a simple transmission
// window with intermittent ACKs from the receiver.
type Conn struct {
	ID    ConnID        // Conn ID
	mux   ConnMuxer     // the Muxer that owns us
	ackCh chan struct{} // ack's from peer go in this

	cmu         sync.Mutex     // state mutex, guards the below variables
	localClosed chan struct{}  // closed when Close() has been called
	onRecycle   func(*Conn)    // function to call when Conn is recycled
	readCh      chan FrameData // data frames from peer go in this
	state       runState       // atomic nonzero if a header frame has been sent or received

	// these are atomic to allow Conn.String() to print the state without causing races
	// localSentFinal int32 // atomic nonzero if local has sent it's final frame
	// remoteSentFinal int32 // atomic nonzero if remote has sent it's final frame
	sendWindow int32 // atomic number of frames still allowed to be in flight
	hijacked   int32 // atomic nonzero if Hijack() was called
	//finalAcked int32 // atomic nonzero if remote ack'd our final frame

	wmu sync.Mutex // guards fdw
	fdw FrameData  // FrameData being written to, nil after final frame sent

	rmu sync.Mutex // guards fdr
	fdr FrameData  // FrameData being read from by fr

	fp            FrameParser // Frame parser (into fdr)
	readDeadline  connDeadline
	writeDeadline connDeadline
	serialNumber  uint32
	netLog        bool // if true, log state changes
}

var connNextSerialNumber uint32

func (conn *Conn) isHijacked() bool {
	return atomic.LoadInt32(&conn.hijacked) != 0
}

func (conn *Conn) hijacking() bool {
	return atomic.CompareAndSwapInt32(&conn.hijacked, 0, 1)
}

func (conn *Conn) starting() bool {
	return atomic.CompareAndSwapInt32((*int32)(&conn.state), int32(runStateIdle), int32(runStateActive))
}

func (conn *Conn) setRunState(state runState) {
	atomic.StoreInt32((*int32)(&conn.state), int32(state))
}

func (conn *Conn) getRunState() runState {
	return runState(atomic.LoadInt32((*int32)(&conn.state)))
}

func (conn *Conn) getRunStateText() string {
	return getRunStateText(conn.getRunState())
}

//func (conn *Conn) hasRemoteSentFinal() bool {
//	return atomic.LoadInt32(&conn.remoteSentFinal) != 0
//}

func (conn *Conn) remoteSendingFinal() bool {
	conn.cmu.Lock()
	defer conn.cmu.Unlock()
	//if conn.hasRemoteSentFinal() {
	//	return false
	//}
	close(conn.readCh)
	//atomic.StoreInt32(&conn.remoteSentFinal, 1)
	return true
}

func (conn *Conn) isClosed() bool {
	select {
	case <-conn.localClosed:
		return true
	default:
		return false
	}
}

//func (conn *Conn) isFinalAcked() bool {
//	return atomic.LoadInt32(&conn.finalAcked) != 0
//}

//func (conn *Conn) mayRecycle() bool {
//	return conn.isFinalAcked() && conn.getRunState() == runStateClosing && conn.isClosed()
//}

//func (conn *Conn) hasLocalSentFinal() bool {
//	return atomic.LoadInt32(&conn.localSentFinal) != 0
//}

// Serial returns a string identifying the Conn, containing the
// owning Muxers serial number, a colon, and this Conn's serial number.
// Note that the serial number is unrelated to the Conn ID.
func (conn *Conn) Serial() string {
	muxSerial := uint32(0)
	if c := conn.getMux(); c != nil {
		muxSerial = c.serialNumber
	}
	return fmt.Sprintf("%x:%04x", muxSerial, conn.serialNumber)
}

func (conn *Conn) String() string {
	hijacked := ""
	if conn.isHijacked() {
		hijacked = " HJ"
	}
	return fmt.Sprintf("[Conn %v %v %s%s (%d+%d)]",
		conn.Serial(), conn.ID, conn.getRunStateText(), hijacked, conn.getSendWindow(), len(conn.ackCh))
}

// NewConn creates a new Conn
func NewConn(mux ConnMuxer, connID ConnID) (conn *Conn) {
	if connID >= MuxerConnID {
		panic(fmt.Sprintf("illegal Conn ID %d", int(connID)))
	}
	conn = &Conn{
		ID:            connID,
		mux:           mux,
		sendWindow:    int32(SendWindowSize),
		ackCh:         make(chan struct{}, MaxSendWindowSize),
		readCh:        make(chan FrameData, MaxSendWindowSize),
		localClosed:   make(chan struct{}),
		readDeadline:  makeConnDeadline(),
		writeDeadline: makeConnDeadline(),
		serialNumber:  atomic.AddUint32(&connNextSerialNumber, 1),
	}
	return
}

// SubmitFrame gives the Conn an incoming FrameData.
// None of the frames seen may be muxer control frames.
// A fd of nil indicates an EOF condition.
// If this function blocks, it will block all Conns on
// the Muxer.
func (conn *Conn) SubmitFrame(fd FrameData) (err error) {
	// log.Print("SubmitFrame() ", conn, fd)

	if fd.Header().HasFlow() {
		if fd.Header().IsAck() {
			FrameDataFree(fd)
			select {
			case conn.ackCh <- struct{}{}:
			default:
				panic(fmt.Sprint("ACK would block"))
			}
		} else if fd.Header().IsFinal() {
			// a final frame or final frame ack
			conn.receivedFinal(fd)
		}
		return
	}

	select {
	case conn.readCh <- fd:
	default:
		panic(fmt.Sprint("DATA would block: ", fd))
	}

	return
}

func (conn *Conn) receivedFinal(fd FrameData) {
	if conn.netLog {
		log.Print(" FIN ", conn, fd)
	}

	if fd.Header().SizeValue() != 0 {
		panic("final frame has size value set")
	}

	if fd.Header().IsFinalAck() {
		switch rs := conn.getRunState(); rs {
		case runStateLocalWaitAck:
			conn.setRunState(runStateLocalClosed)
		case runStateWaitAck:
			conn.setRunState(runStateRecycle)
		default:
			panic(fmt.Sprint("got final ack in state ", getRunStateText(rs)))
		}
	} else if !conn.remoteSendingFinal() {
		panic("received multiple final frames")
	}

	FrameDataFree(fd)
}

func (conn *Conn) readEOF() error {
	// send final frame ack
	conn.writeFinal(true)

	switch rs := conn.getRunState(); rs {
	case runStateActive:
		conn.setRunState(runStateRemoteClosed)
	case runStateLocalWaitAck:
		conn.setRunState(runStateWaitAck)
	case runStateLocalClosed:
		conn.setRunState(runStateRecycle)
	case runStateRemoteClosed:
		// let it return EOF again
	default:
		panic(fmt.Sprint("got final frame in runstate ", getRunStateText(rs)))
	}

	return errors.WithStack(io.EOF)
}

// readFrame reads data frames from the read channel.
// None of the frames seen may be muxer control frames.
// Also writes acknowledgement frames.
func (conn *Conn) readFrame() error {
	// log.Print("Conn.readFrame(): len(conn.fdr)=", len(conn.fdr), " conn=", conn)
	if conn.fdr != nil {
		FrameDataFree(conn.fdr)
		conn.fdr = nil
		conn.fp = nil
	}

	select {
	case conn.fdr = <-conn.readCh:
		if conn.fdr == nil {
			return conn.readEOF()
		}
		conn.fp = NewFrameParser(conn.fdr)
		conn.writeAckFrame()
	case <-conn.readDeadline.wait():
		return errors.WithStack(timeoutError{})
	case <-conn.mux.ConnAbortChannel():
		return errors.WithStack(serverClosedError{})
	}

	return nil
}

func (conn *Conn) writeAckFrame() {
	fd := FrameDataAllocID(conn.ID)
	fd.Header().SetFlow()
	conn.mux.ConnWrite(fd)
}

// LoadFrameReader ensures the frame reader has payload data or an error.
func (conn *Conn) LoadFrameReader() (err error) {
	conn.rmu.Lock()
	defer conn.rmu.Unlock()
	return conn.loadFrameReader()
}

func (conn *Conn) loadFrameReader() (err error) {
	for len(conn.fp) == 0 && err == nil {
		err = conn.readFrame()
		// log.Print("Conn.loadFrameReader(): ", conn.ID, " readFrame() len(fr)=", len(conn.fp), " err=", err, "  ", conn)
	}
	return
}

// WriteStart prepares a new frame for writing.
func (conn *Conn) WriteStart() error {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	return conn.writeStart()
}

func (conn *Conn) writeStart() error {
	//if conn.hasRemoteSentFinal() {
	//	return errors.WithStack(io.EOF)
	//}
	select {
	case <-conn.localClosed:
		return errors.WithStack(io.ErrClosedPipe)
	case <-conn.writeDeadline.wait():
		return errors.WithStack(timeoutError{})
	default:
		if conn.fdw == nil {
			// log.Print("Conn.writeStart() (new fd)", conn)
			conn.fdw = FrameDataAllocID(conn.ID)
		}
		return nil
	}
}

// Available returns number of free bytes in the current frame.
func (conn *Conn) Available() int {
	return conn.fdw.Available()
}

// Buffered returns the number of bytes that have been written to the
// current frame, including the header size.
func (conn *Conn) Buffered() int {
	return conn.fdw.Buffered()
}

// Implements io.Reader for Conn.
// Used when copying data from a Muxer to a HTTP body.
func (conn *Conn) Read(p []byte) (n int, err error) {
	conn.rmu.Lock()
	defer conn.rmu.Unlock()
	return conn.read(p)
}

func (conn *Conn) read(p []byte) (n int, err error) {
	if err = conn.loadFrameReader(); err == nil {
		n, err = conn.fp.Read(p)
	}
	return
}

// ReadFrom implements io.ReaderFrom for Conn body data.
// Used when copying data from a HTTP body to a Muxer.
func (conn *Conn) ReadFrom(r io.Reader) (n int64, err error) {
	if r == nil {
		return 0, errors.WithStack(io.EOF)
	}
	for err == nil {
		var m int64
		m, err = conn.readFromHelper(r)
		n += m
	}
	// io.ReaderFrom: Any error except io.EOF encountered during the read is also returned.
	if errors.Cause(err) == io.EOF {
		err = nil
	}
	return
}

func (conn *Conn) readFrom(r io.Reader) (n int64, err error) {
	for err == nil {
		var m int64
		m, err = conn.readFromHelperLocked(r)
		n += m
	}
	// io.ReaderFrom: Any error except io.EOF encountered during the read is also returned.
	if errors.Cause(err) == io.EOF {
		err = nil
	}
	return
}

func (conn *Conn) readFromHelper(r io.Reader) (n int64, err error) {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	return conn.readFromHelperLocked(r)
}

func (conn *Conn) readFromHelperLocked(r io.Reader) (n int64, err error) {
	var count int
	if err = conn.writeStart(); err == nil {
		maxCount := conn.fdw.Available()
		if count, err = r.Read(conn.fdw[len(conn.fdw) : len(conn.fdw)+maxCount]); count > 0 {
			if err != nil {
				err = errors.WithStack(err)
			}
			conn.fdw.Header().SetBody()
			n += int64(count)
			conn.fdw = conn.fdw[:len(conn.fdw)+count]
			if flushErr := conn.flushLocked(); flushErr != nil {
				if err == nil {
					err = flushErr
				}
			}
		}
	}
	return
}

// TODO: func (b *Writer) Reset(w io.Writer)

// Write implements io.Writer for Conn, and is used to write body data.
func (conn *Conn) Write(p []byte) (n int, err error) {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	// log.Print("Conn.Write() len(p)=", len(p), " avail=", conn.Available())
	if n, err = conn.write(p); err == nil {
		if err = conn.flushLocked(); err != nil {
			n = 0
		}
	}
	return
}

func (conn *Conn) write(p []byte) (n int, err error) {
	err = conn.writeStart()

	for err == nil && len(p) > conn.fdw.Available() {
		var m int
		if m = conn.fdw.Available(); m > 0 {
			conn.fdw.Header().SetBody()
			conn.fdw = append(conn.fdw, p[:m]...)
			p = p[m:]
		}
		if err = conn.flushLocked(); err == nil {
			n += m
			err = conn.writeStart()
		}
	}

	if err == nil {
		conn.fdw.Header().SetBody()
		conn.fdw = append(conn.fdw, p...)
		n += len(p)
	}

	return
}

// WriteTo writes the body payload of the Conn to a io.Writer.
func (conn *Conn) WriteTo(w io.Writer) (n int64, err error) {
	for err == nil {
		var m int64
		m, err = conn.writeToHelper(w)
		n += m
	}
	if errors.Cause(err) == io.EOF {
		err = nil
	}
	return
}

func (conn *Conn) writeToHelper(w io.Writer) (n int64, err error) {
	conn.rmu.Lock()
	defer conn.rmu.Unlock()
	if err = conn.loadFrameReader(); err != nil {
		return
	}
	for len(conn.fp) > 0 {
		var count int
		count, err = w.Write(conn.fp)
		conn.fp = conn.fp[count:]
		n += int64(count)
		if err != nil && err != io.ErrShortWrite {
			return
		}
	}
	return
}

// WriteByte implements io.ByteWriter for Conn.
func (conn *Conn) WriteByte(c byte) (err error) {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	if err = conn.writeByte(c); err == nil {
		err = conn.flushLocked()
	}
	return
}

func (conn *Conn) writeByte(c byte) (err error) {
	err = conn.writeStart()

	if err == nil && conn.fdw.Available() <= 0 {
		if err = conn.flushLocked(); err == nil {
			err = conn.writeStart()
		}
	}

	if err == nil {
		conn.fdw.Header().SetBody()
		err = conn.fdw.WriteByte(c)
	}

	return
}

// TODO: func (b *Writer) WriteRune(r rune) (size int, err error)
// TODO: func (b *Writer) WriteString(s string) (int, error)

// Flush handles write flow control and injects the current frame into the Muxer.
// Note that the current write frame is expected to be a regular data frame,
// such that conn.fdw.Header() returns false for IsMuxerControl() and true for
// HasPayload().
func (conn *Conn) Flush() (err error) {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	return conn.flushLocked()
}

func (conn *Conn) flushLocked() (err error) {
	if fd := conn.fdw; fd != nil {
		conn.fdw = nil
		err = conn.writeFrame(fd)
	}
	return
}

func (conn *Conn) writeFrame(fd FrameData) (err error) {
	if fd.Header().HasFlow() {
		panic(fmt.Sprint("attempt to send flow control frame: ", conn, fd))
	}

	if !fd.Header().HasBodyOrHead() {
		if len(fd) > FrameHeaderSize {
			panic(fmt.Sprint("missing head or body flag: ", conn, fd))
		}
		// empty blank frame
		FrameDataFree(fd)
		return
	}

	if len(fd) > FrameMaxSize {
		return ErrFrameTooBig{}
	}

	fd.Header().SetSizeValue(len(fd) - FrameHeaderSize)

	// Consume ACKs and check for close conditions
	for err == nil {
		select {
		case <-conn.ackCh:
			conn.consumeAck()
		case <-conn.localClosed:
			err = errors.WithStack(io.ErrClosedPipe)
		case <-conn.mux.ConnAbortChannel():
			err = errors.WithStack(serverClosedError{})
		case <-conn.writeDeadline.wait():
			err = errors.WithStack(timeoutError{})
		default:
			//if conn.hasRemoteSentFinal() {
			//	err = errors.WithStack(io.EOF)
			//} else
			if conn.getSendWindow() > 0 {
				// if the send window allows, go ahead and send it
				if atomic.AddInt32(&conn.sendWindow, -1) < 0 {
					panic(fmt.Sprintf("sendWindow went negative: %+v\n", conn))
				}
				conn.starting()
				return conn.mux.ConnWrite(fd)
			}
			// SendWindow is empty, so do a blocking wait for an ACK
			select {
			case <-conn.ackCh:
				conn.consumeAck()
			case <-conn.localClosed:
				err = errors.WithStack(io.ErrClosedPipe)
			case <-conn.mux.ConnAbortChannel():
				err = errors.WithStack(serverClosedError{})
			case <-conn.writeDeadline.wait():
				err = errors.WithStack(timeoutError{})
			}
		}
	}

	FrameDataFree(fd)
	return
}

func (conn *Conn) getSendWindow() int {
	return int(atomic.LoadInt32(&conn.sendWindow))
}

func (conn *Conn) getMux() *Muxer {
	if c, ok := conn.mux.(*Muxer); ok {
		return c
	}
	return nil
}

// recycle restores an Conn to it's initial state and calls the onRecycle handler.
// Before calling, the local end must be closed. If the Conn is started,
// then remote must be closed as well. If it is not started, the remote must not be closed.
func (conn *Conn) recycle() {
	conn.cmu.Lock()
	defer conn.cmu.Unlock()
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	conn.rmu.Lock()
	defer conn.rmu.Unlock()
	conn.recycleLocked()
}

func (conn *Conn) recycleLocked() {
	if conn.netLog {
		log.Print("  RC ", conn)
	}

	if err := conn.canRecycleLocked(); err != nil {
		panic(err)
	}

	conn.setRunState(runStateRecycle)
	conn.forceRecycleLocked()
}

func (conn *Conn) canRecycleLocked() error {
	if rs := conn.getRunState(); rs != runStateLocalClosed && rs != runStateWaitAck {
		return errors.Errorf("recycle(): not in closing state\n%v\n", conn)
	}

	switch {
	case !isClosedChan(conn.localClosed):
		return errors.Errorf("recycle(): local not closed\n%v\n", conn)
	case len(conn.fdw) > 0:
		return errors.Errorf("recycle(): still data left in fdw\n%v\n%v\n", conn, conn.fdw)
	}
	return nil
}

func (conn *Conn) invokeOnRecycleLocked() {
	if conn.onRecycle != nil {
		conn.onRecycle(conn)
	}
}

func (conn *Conn) forceRecycleLocked() {
	// drain ack and read channels
	for len(conn.ackCh) > 0 {
		<-conn.ackCh
	}
	for len(conn.readCh) > 0 {
		FrameDataFree(<-conn.readCh)
	}

	conn.readCh = make(chan FrameData, MaxSendWindowSize)
	conn.localClosed = make(chan struct{})
	// atomic.StoreInt32(&conn.localSentFinal, 0)
	// atomic.StoreInt32(&conn.remoteSentFinal, 0)
	atomic.StoreInt32(&conn.sendWindow, int32(SendWindowSize))
	atomic.StoreInt32(&conn.hijacked, 0)
	conn.fdw = nil
	conn.fdr = nil
	conn.fp = nil
	conn.writeDeadline.set(time.Time{})
	conn.readDeadline.set(time.Time{})

	conn.setRunState(runStateIdle)
	conn.invokeOnRecycleLocked()
}

func (conn *Conn) consumeAck() {
	if atomic.AddInt32(&conn.sendWindow, 1) > int32(SendWindowSize) {
		panic(fmt.Sprintf("sendWindow %d > %d SendWindowSize: %+v\n", conn.getSendWindow(), int32(SendWindowSize), conn))
	}
}

func (conn *Conn) makeFinalFrame(isAck bool) (fd FrameData) {
	if fd = FrameDataAllocID(conn.ID); fd != nil {
		fd.Header().SetFlow()
		fd.Header().SetBody()
		if isAck {
			fd.Header().SetHead()
		}
		return
	}
	panic("failed to allocate final frame")
}

// need wmu locked
func (conn *Conn) writeFinalLocked(isAck bool) error {
	conn.fdw = nil // discard any incomplete frame
	return conn.mux.ConnWrite(conn.makeFinalFrame(isAck))
}

func (conn *Conn) writeFinal(isAck bool) error {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	return conn.writeFinalLocked(isAck)
}

// need cmu locked
func (conn *Conn) shutdownLocked() error {
	var newRunState runState

	switch rs := conn.getRunState(); rs {
	case runStateIdle, runStateLocalClosed, runStateLocalWaitAck, runStateWaitAck:
		return nil
	case runStateActive:
		newRunState = runStateLocalWaitAck
	case runStateRemoteClosed:
		newRunState = runStateWaitAck
	default:
		panic(fmt.Sprint("Close() called in state ", getRunStateText(rs)))
	}

	err := conn.writeFinal(false)
	if err == nil {
		conn.setRunState(newRunState)
	}
	return err
}

// Shutdown sends the final frame signalling this Conn won't send any more data,
// Any future writes will fail until the Conn is closed and recycled.
// You may keep reading data on the Conn.
func (conn *Conn) Shutdown() error {
	conn.cmu.Lock()
	defer conn.cmu.Unlock()
	return conn.shutdownLocked()
}

func (conn *Conn) close() (err error) {
	conn.cmu.Lock()
	defer conn.cmu.Unlock()

	switch rs := conn.getRunState(); rs {
	case runStateIdle:
		// Conn was taken from a Muxer but never used.
		conn.invokeOnRecycleLocked()
		return nil
	case runStateActive:
		// send final frame
		// wait for final frame ack
		// for remote to send final frame
		err = conn.shutdownLocked()
	case runStateRemoteClosed:
		// send final frame
		// wait for final frame ack
		err = conn.shutdownLocked()
	case runStateLocalWaitAck:
		// wait for both final frame ack and
		// for remote to send final frame
	case runStateLocalClosed:
		// wait for remote to send final frame
	case runStateWaitAck:
		// remote closed, wait for final frame ack
	case runStateRecycle:
		// already trying to recycle
		return nil
	default:
		// should never occur
		panic(fmt.Sprint("Close() called in unknown state ", getRunStateText(rs)))
	}

	close(conn.localClosed)
	return
}

// Close interrupts any write operations and blocks until the conn is recycled.
// It will discard incoming data frames on the conn.
func (conn *Conn) Close() (err error) {
	if err = conn.close(); err != nil {
		return err
	}

	// wait until state reaches runStateRecycle
	timeLeft := 1000
	for conn.getRunState() != runStateRecycle {
		time.Sleep(time.Millisecond)
		timeLeft--
		if timeLeft < 0 {
			return errors.WithStack(timeoutError{})
		}
	}
	return
}

// OnRecycle sets the callback to be invoked when the Conn is being recycled.
// Set to nil to disable the callback. You may *not* call this function from the
// callback itself, as that will deadlock.
func (conn *Conn) OnRecycle(onRecycle func(*Conn)) {
	conn.cmu.Lock()
	defer conn.cmu.Unlock()
	conn.onRecycle = onRecycle
}

// WriteUserRecordType writes a user record marker and sets the head bit.
func (conn *Conn) WriteUserRecordType(c byte) (err error) {
	if c < 0x80 {
		return errors.WithStack(ErrUnhandledRecordType{RecordType(c)})
	}
	if err = conn.WriteStart(); err == nil {
		conn.fdw.WriteRecordType(RecordType(c))
	}
	return
}

func (conn *Conn) writeRequest(r *http.Request) (err error) {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	if conn.fdw == nil {
		return errors.WithStack(io.EOF)
	}
	return conn.fdw.WriteRequest(r)
}

// WriteRequest writes a http.Request to the Conn, including it's Body.
func (conn *Conn) WriteRequest(r *http.Request) (err error) {
	if err = conn.WriteStart(); err == nil {
		if err = conn.writeRequest(r); err == nil {
			if r.Body != nil {
				if r.ContentLength > 0 {
					if _, err = io.CopyN(conn, r.Body, r.ContentLength); err != nil {
						err = errors.WithStack(err)
					}
				} else if r.ContentLength == -1 {
					_, err = conn.ReadFrom(r.Body)
				}
				r.Body.Close()
			}
			if flushErr := conn.Flush(); err == nil {
				err = flushErr
			}
		}
	}
	return
}

// ProxyResponse reads a HTTP response but not it's body from the Conn data
// and writes it to the given http.ResponseWriter.
func (conn *Conn) ProxyResponse(w http.ResponseWriter) (statusCode int, err error) {
	conn.rmu.Lock()
	defer conn.rmu.Unlock()

	if err = conn.loadFrameReader(); err == nil {
		if conn.fdr.Header().HasHead() {
			switch rt := conn.fp.ReadRecordType(); rt {
			case RecordTypeHTTPResponse:
				statusCode = conn.fp.ProxyResponse(w)
			case RecordTypeHijacked:
				statusCode = 101
			default:
				err = errors.WithStack(ErrUnhandledRecordType{rt})
			}
		}
	}
	return
}

// WriteResponse writes a http.Response to the Conn.
func (conn *Conn) WriteResponse(r *http.Response) (err error) {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	if err = conn.writeResponseDataLocked(r.StatusCode, r.ContentLength, r.Header); err == nil {
		if err == nil && r.Body != nil {
			if r.ContentLength > 0 {
				_, err = io.CopyN(conn, r.Body, r.ContentLength)
			} else if r.ContentLength == -1 {
				_, err = conn.readFrom(r.Body)
			} else {
				r.Body.Close()
			}
		}
		if flushErr := conn.flushLocked(); err == nil {
			err = flushErr
		}
	}
	return
}

// WriteResponseData writes a RAP response header.
func (conn *Conn) WriteResponseData(code int, contentLength int64, header http.Header) (err error) {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	return conn.writeResponseDataLocked(code, contentLength, header)
}

func (conn *Conn) writeResponseDataLocked(code int, contentLength int64, header http.Header) (err error) {
	if err = conn.writeStart(); err == nil {
		err = conn.fdw.WriteResponse(code, contentLength, header)
	}
	return
}

func (conn *Conn) discardAndCloseIfNotHijacked() error {
	if conn.isHijacked() {
		return nil
	}
	for conn.LoadFrameReader() == nil {
	}
	return conn.Close()
}

// Serve waits for a start frame and then invokes the given http.Handler.
func (conn *Conn) Serve(h http.Handler) (err error) {
	defer conn.discardAndCloseIfNotHijacked()
	if err = conn.LoadFrameReader(); err == nil {
		if conn.fdr.Header().HasHead() {
			switch rt := conn.fp.ReadRecordType(); rt {
			case RecordTypeHTTPRequest:
				var req *http.Request
				req, err = conn.fp.ReadRequest()
				if err == nil {
					// log.Printf("rap.Conn.Serve(): %+v\n", req)
					req.Body = conn
					h.ServeHTTP(&ResponseWriter{Conn: conn}, req)
					// if the handler left things in the buffer, flush it
					if flushErr := conn.Flush(); err == nil {
						err = flushErr
					}
				}
			case RecordTypeHijacked:
				conn.hijacking()
			default:
				err = errors.WithStack(ErrUnhandledRecordType{rt})
			}
		} else {
			err = errors.Wrapf(ErrMissingFrameHead{}, "%v", conn.fdr)
		}
	}
	return
}

// Hijack lets the caller take over the connection.
// After a call to Hijack the HTTP server library
// will not do anything else with the connection.
func (conn *Conn) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn.wmu.Lock()
	defer conn.wmu.Unlock()
	if conn.hijacking() {
		conn.starting()
		fd := NewFrameDataID(conn.ID)
		fd.WriteRecordType(RecordTypeHijacked)
		return conn, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)), conn.writeFrame(fd)
	}
	return nil, nil, errors.Errorf("already hijacked")
}

type connAddr struct{}

func (connAddr) Network() string { return "rap" }
func (connAddr) String() string  { return "rap" }

// LocalAddr returns the local network address stub.
func (conn *Conn) LocalAddr() net.Addr {
	return connAddr{}
}

// RemoteAddr returns remote network address stub.
func (conn *Conn) RemoteAddr() net.Addr {
	return connAddr{}
}

// SetDeadline sets the read and write deadlines associated
// with the connection. It is equivalent to calling both
// SetReadDeadline and SetWriteDeadline.
func (conn *Conn) SetDeadline(t time.Time) error {
	if conn.isClosed() {
		return errors.WithStack(io.ErrClosedPipe)
	}
	conn.readDeadline.set(t)
	conn.writeDeadline.set(t)
	return nil
}

// SetReadDeadline sets the deadline for future Read calls
// and any currently-blocked Read call.
// A zero value for t means Read will not time out.
func (conn *Conn) SetReadDeadline(t time.Time) error {
	if conn.isClosed() {
		return errors.WithStack(io.ErrClosedPipe)
	}
	conn.readDeadline.set(t)
	return nil
}

// SetWriteDeadline sets the deadline for future Write calls
// and any currently-blocked Write call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means Write will not time out.
func (conn *Conn) SetWriteDeadline(t time.Time) error {
	if conn.isClosed() {
		return errors.WithStack(io.ErrClosedPipe)
	}
	conn.writeDeadline.set(t)
	return nil
}
