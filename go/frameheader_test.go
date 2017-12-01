package rap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const testExchangeID ExchangeID = 0x1234

func getHeader(t *testing.T) (h FrameHeader) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	assert.NotNil(t, fd)
	assert.Equal(t, FrameHeaderSize, len(fd))
	h = fd.Header()
	assert.NotNil(t, h)
	return
}

func Test_FrameHeader_IsBlank(t *testing.T) {
	h := getHeader(t)
	assert.Equal(t, ExchangeID(0), h.ExchangeID())
	assert.False(t, h.HasBody())
	assert.False(t, h.HasHead())
	assert.False(t, h.HasPayload())
	assert.False(t, h.IsFinal())
	assert.False(t, h.IsConnControl())
}

func Test_FrameHeader_ExchangeIDRange(t *testing.T) {
	h := getHeader(t)
	h.Clear()
	assert.Equal(t, ExchangeID(0), h.ExchangeID())
	h.SetExchangeID(ExchangeID(1))
	assert.Equal(t, ExchangeID(1), h.ExchangeID())
	h.SetExchangeID(MaxExchangeID)
	assert.Equal(t, MaxExchangeID, h.ExchangeID())
	assert.Panics(t, func() { h.SetExchangeID(MaxExchangeID + 1) })
}

func Test_FrameHeader_String(t *testing.T) {
	h := getHeader(t)
	assert.Equal(t, "[FrameHeader [ExchangeID 0000] ... 0 (4)]", h.String())
	h.SetHead()
	h.SetBody()
	h.SetFinal()
	h.SetSizeValue(12)
	h.SetExchangeID(444)
	assert.Equal(t, "[FrameHeader [ExchangeID 01bc] FHB 12 (4)]", h.String())
	assert.Equal(t, 12, h.PayloadSize())
	h.SetConnControl(ConnControlPing)
	assert.Equal(t, "[FrameHeader [ExchangeID 1fff] Ping 12 (4)]", h.String())
	assert.Equal(t, 0, h.PayloadSize())
}
