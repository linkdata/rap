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
