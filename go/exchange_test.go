package rap

import (
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
	et := newExchangeTester(t)
	et.InjectRequest(httptest.NewRequest("GET", "/", nil))
	et.Exchange.Start(et)
	et.Exchange.Release()
	assert.True(t, et.released)
}
