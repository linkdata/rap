package rap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_FrameReader_String(t *testing.T) {
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

func Test_FrameReader_Read(t *testing.T) {
	fd := NewFrameData()
	fd.WriteString("quuxFooBAR")
	fr := NewFrameReader(fd)
	ba := make([]byte, 11)
	fr.Read(ba)
	assert.Equal(t, []byte{0xa, 0x71, 0x75, 0x75, 0x78, 0x46, 0x6f, 0x6f, 0x42, 0x41, 0x52}, ba)
}

func Test_FrameReader_ReadRequest_IllegalURL(t *testing.T) {
	fd := NewFrameData()
	fd.WriteStringNull()  // method
	fd.WriteString(":a:") // illegal url
	fr := NewFrameReader(fd)
	req, err := fr.ReadRequest()
	assert.Nil(t, req)
	assert.Error(t, err)
}
