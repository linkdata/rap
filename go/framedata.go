package rap

import (
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	// ErrLengthNegative is returned for strings with negative length.
	ErrLengthNegative = errors.New("length negative")
	// ErrLengthOverflow is returned for strings longer than 32K.
	ErrLengthOverflow = errors.New("length overflow")
)

// FrameData is a byte array used as a network data frame.
type FrameData []byte

func (fd FrameData) String() string {
	var contents string
	if len(fd) > 32 {
		contents = hex.EncodeToString(fd[:32]) + "..."
	} else {
		hex.EncodeToString(fd)
	}
	return fmt.Sprintf("[FrameData %v %v]", fd.Header(), contents)
}

// Header returns the FrameHeader part of a FrameData.
func (fd FrameData) Header() FrameHeader {
	return FrameHeader(fd)
}

// Payload returns the payload of a FrameData as a byte slice.
func (fd FrameData) Payload() []byte {
	return fd[FrameHeaderSize:]
}

// Available returns number of free bytes in the FrameData.
func (fd FrameData) Available() int {
	return cap(fd) - len(fd)
}

// Buffered returns the number of bytes that have been written to the frame.
func (fd FrameData) Buffered() int {
	return len(fd)
}
