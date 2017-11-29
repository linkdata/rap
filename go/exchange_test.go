package rap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

type exchangeTester struct {
	t        *testing.T
	released bool
	writeCh  chan FrameData
	readCh   chan FrameData
	*Exchange
}

func newExchangeTester(t *testing.T) *exchangeTester {
	et := &exchangeTester{
		t:       t,
		writeCh: make(chan FrameData),
		readCh:  make(chan FrameData, MaxSendWindowSize),
	}
	et.Exchange = NewExchange(et, 0x123)

	go func() {
		for {
			fd := <-et.writeCh
			if fd == nil {
				break
			}
			fd.Clear()
		}
	}()

	return et
}

func (et *exchangeTester) EnsureFlushError() {

}

func (et *exchangeTester) ExchangeWriteChannel() chan FrameData {
	return et.writeCh
}

func (et *exchangeTester) ExchangeReadChannel() chan FrameData {
	return et.readCh
}

func (et *exchangeTester) ExchangeRelease(e *Exchange) {
	et.released = true
	close(et.writeCh)
}

func (et *exchangeTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

func (et *exchangeTester) InjectRequest(req *http.Request) {
	fd := NewFrameData()
	fd.Header().SetFinal()
	if err := fd.WriteRequest(req); err != nil {
		assert.NoError(et.t, err)
	}
	et.readCh <- fd
}

func Test_Exchange_String(t *testing.T) {
	e := NewExchange(&exchangeTester{}, 0x123)
	assert.Equal(t, "[Exchange [ExchangeID 0123] sendW=8 started=false sentC=false recvC=false len(readCh)=0 len(ackCh)=0]", e.String())
}

func Test_Exchange_StartAndRelease(t *testing.T) {
	// Simple case
	et := newExchangeTester(t)
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	et.Exchange.Start(et)
	et.Exchange.Release()
	assert.True(t, et.released)

	// EOF before starting
	et = newExchangeTester(t)
	close(et.readCh)
	err := et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Empty frame
	et = newExchangeTester(t)
	fd := NewFrameData()
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Empty frame sequence
	et = newExchangeTester(t)
	fd = NewFrameData()
	et.readCh <- fd
	fd = NewFrameData()
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Equal(t, io.EOF, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Missing frame head
	et = newExchangeTester(t)
	fd = NewFrameData()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Equal(t, ErrMissingFrameHead, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Invalid URL in request record
	et = newExchangeTester(t)
	fd = NewFrameData()
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.WriteStringNull()  // method
	fd.WriteString(":a:") // illegal url
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Error(t, err)
	et.Exchange.Release()
	assert.True(t, et.released)

	// Incomplete request frame
	et = newExchangeTester(t)
	fd = NewFrameData()
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.Header().SetFinal()
	et.readCh <- fd
	assert.Panics(t, func() {
		et.Exchange.Start(et)
	})
	et.Exchange.Release()
	assert.True(t, et.released)

	// Illegal record type
	et = newExchangeTester(t)
	fd = NewFrameData()
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeUserFirst - 1)
	fd.Header().SetFinal()
	et.readCh <- fd
	err = et.Exchange.Start(et)
	assert.Equal(t, ErrUnhandledRecordType, err)
	et.Exchange.Release()
	assert.True(t, et.released)
}

func Test_Exchange_WriteByte(t *testing.T) {
	et := newExchangeTester(t)

	// HasBody is true after writing
	et.Exchange.WriteByte(0x01)
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())

	// Fill up to limit
	et.Exchange.fdw.Header().SetFinal()
	et.Exchange.Write(make([]byte, et.Exchange.Available()))
	assert.Equal(t, FrameMaxSize, et.Exchange.Buffered())
	assert.True(t, et.Exchange.fdw.Header().HasBody())

	// Write one more should flush
	assert.NoError(t, et.Exchange.WriteByte(0x01))
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())

	// Force a flush, should fail since final is sent
	n, err := et.Exchange.Write(make([]byte, et.Exchange.Available()))
	assert.NoError(t, err)
	assert.NotZero(t, n)
	err = et.Exchange.WriteByte(0x01)
	assert.Equal(t, io.ErrClosedPipe, err)
}

func Test_Exchange_Write(t *testing.T) {
	et := newExchangeTester(t)

	// Empty after writeStart()
	et.Exchange.writeStart()
	assert.False(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameMaxPayloadSize, et.Exchange.Available())

	// HasBody is true after writing
	et.Exchange.WriteByte(0x01)
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())

	// Fill up to just under limit
	et.Exchange.Write(make([]byte, et.Exchange.Available()-1))
	assert.Equal(t, FrameMaxSize-1, et.Exchange.Buffered())
	assert.True(t, et.Exchange.fdw.Header().HasBody())

	// Set final and write one more byte than can be fit
	et.Exchange.fdw.Header().SetFinal()
	et.Exchange.Write([]byte{0x04, 0x05})
	assert.True(t, et.Exchange.fdw.Header().HasBody())
	assert.Equal(t, FrameHeaderSize+1, et.Exchange.Buffered())

	// Force a flush, should fail since final is sent
	assert.Equal(t, io.ErrClosedPipe, et.Exchange.Flush())
}

func Test_Exchange_Flush(t *testing.T) {
	et := newExchangeTester(t)

	// Normal flush
	n, err := et.Exchange.Write([]byte{0x01, 0x02})
	assert.NoError(t, err)
	assert.Equal(t, 2, n)
	err = et.Exchange.Flush()
	assert.NoError(t, err)

	// Flush after Flush is a no-op
	err = et.Exchange.Flush()
	assert.NoError(t, err)

	// Overflow the frame and flush should error
	et.Exchange.writeStart()
	et.Exchange.fdw = append(et.Exchange.fdw, make([]byte, FrameMaxPayloadSize+1)...)
	assert.Equal(t, FrameMaxSize+1, len(et.Exchange.fdw))
	err = et.Exchange.Flush()
	assert.Equal(t, ErrFrameTooBig, err)
}
