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
	assert.Equal(t, ConnID(0), h.ConnID())
	assert.False(t, h.HasBody())
	assert.False(t, h.HasHead())
	assert.False(t, h.HasPayload())
	assert.False(t, h.HasFlow())
	assert.False(t, h.IsMuxerControl())
}

func Test_FrameHeader_ConnIDRange(t *testing.T) {
	h := getHeader(t)
	h.Clear()
	assert.Equal(t, ConnID(0), h.ConnID())
	h.SetConnID(ConnID(1))
	assert.Equal(t, ConnID(1), h.ConnID())
	h.SetConnID(MaxConnID)
	assert.Equal(t, MaxConnID, h.ConnID())
	assert.Panics(t, func() { h.SetConnID(MuxerConnID + 1) })
}

func Test_FrameHeader_String(t *testing.T) {
	h := getHeader(t)
	assert.Equal(t, "[FrameHeader [ID 0000] ... 0 (4)]", h.String())
	h.SetHead()
	h.SetBody()
	h.SetFlow()
	h.SetSizeValue(12)
	h.SetConnID(1)
	assert.Equal(t, "[FrameHeader [ID 0001] FBH 12 (4)]", h.String())
	assert.Equal(t, 0, h.PayloadSize()) // zero since Flow is set
	h.SetMuxerControl(MuxerControlPing)
	expected := fmt.Sprintf("[FrameHeader %v Ping 12 (4)]", ConnID(MuxerConnID))
	assert.Equal(t, expected, h.String())
	assert.Equal(t, 12, h.PayloadSize())
}
