package rap

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

type exchangeTester struct {
	released bool
	writeCh  chan FrameData
	readCh   chan FrameData
}

func newExchangeTester() *exchangeTester {
	return &exchangeTester{
		writeCh: make(chan FrameData),
		readCh:  make(chan FrameData, MaxSendWindowSize),
	}
}

func (et *exchangeTester) ExchangeWriteChannel() chan FrameData {
	return et.writeCh
}

func (et *exchangeTester) ExchangeReadChannel() chan FrameData {
	return et.readCh
}

func (et *exchangeTester) ExchangeRelease(e *Exchange) {
	et.released = true
}

func (et *exchangeTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

func Test_Exchange_String(t *testing.T) {
	e := NewExchange(&exchangeTester{}, 0x123)
	assert.Equal(t, "[Exchange [ExchangeID 0123] sendW=8 started=false sentC=false recvC=false len(readCh)=0 len(ackCh)=0]", e.String())
}

func Test_Exchange_StartAndRelease(t *testing.T) {
	var e *Exchange
	et := newExchangeTester()
	e = NewExchange(et, 0x123)
	e.Start(et)
	e.Release()
	assert.True(t, et.released)
}
