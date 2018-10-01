package rap

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
	assert.False(t, h.HasFlow())
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
	assert.Panics(t, func() { h.SetExchangeID(ConnExchangeID + 1) })
}

func Test_FrameHeader_String(t *testing.T) {
	h := getHeader(t)
	assert.Equal(t, "[FrameHeader [ID 0000] ... 0 (4)]", h.String())
	h.SetHead()
	h.SetBody()
	h.SetFlow()
	h.SetSizeValue(12)
	h.SetExchangeID(1)
	assert.Equal(t, "[FrameHeader [ID 0001] FBH 12 (4)]", h.String())
	assert.Equal(t, 0, h.PayloadSize()) // zero since Flow is set
	h.SetConnControl(ConnControlPing)
	expected := fmt.Sprintf("[FrameHeader %v Ping 12 (4)]", ExchangeID(ConnExchangeID))
	assert.Equal(t, expected, h.String())
	assert.Equal(t, 12, h.PayloadSize())
}
