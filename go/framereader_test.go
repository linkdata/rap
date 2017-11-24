package rap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFrameReaderString(t *testing.T) {
	fd := NewFrameData()
	fr := NewFrameReader(fd)
	assert.Equal(t, "[FrameReader 0]", fr.String())
	fd.WriteByte(0x01)
	fr = NewFrameReader(fd)
	assert.Equal(t, "[FrameReader 1 01]", fr.String())
	fd.WriteString("Hello world with a longer string")
	fr = NewFrameReader(fd)
	assert.Equal(t, "[FrameReader 34 012048656c6c6f20776f726c6420776974682061206c6f6e6765722073747269...]", fr.String())
}
