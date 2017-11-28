package rap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_Exchange_String(t *testing.T) {
	e := NewExchange(nil, nil, 0x123)
	assert.Equal(t, "[Exchange [ExchangeID 0123] sendW=8 started=false sentC=false recvC=false len(readCh)=0 len(ackCh)=0]", e.String())
}
