package rap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const testExchangeID ExchangeID = 0x1234

func getHeader(t *testing.T) (h FrameHeader) {
	fd := NewFrameData()
	assert.NotNil(t, fd)
	h = fd.Header()
	assert.NotNil(t, h)
	return
}

func TestFrameDataHeaderIsBlank(t *testing.T) {
	h := getHeader(t)
	assert.Equal(t, ExchangeID(0), h.ExchangeID())
	assert.False(t, h.HasBody())
	assert.False(t, h.HasHead())
	assert.False(t, h.HasPayload())
	assert.False(t, h.IsFinal())
	assert.False(t, h.IsConnControl())
}

func TestFrameDataHeaderExchangeIDRange(t *testing.T) {
	h := getHeader(t)
	assert.Equal(t, ExchangeID(0), h.ExchangeID())
	h.SetExchangeID(ExchangeID(1))
	assert.Equal(t, ExchangeID(1), h.ExchangeID())
	h.SetExchangeID(MaxExchangeID)
	assert.Equal(t, MaxExchangeID, h.ExchangeID())
	assert.Panics(t, func() { h.SetExchangeID(MaxExchangeID + 1) })
}
