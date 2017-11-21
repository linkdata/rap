package rap

import (
	"errors"
	"io"
	"log"
)

// WriteHeader initializes the frame header.
func (fd *FrameData) WriteHeader(exchangeID ExchangeID) {
	*fd = AppendFrameHeader((*fd)[:0], exchangeID)
	return
}

// WriteUint64 writes an uint64 to a FrameData using a portable encoding.
func (fd *FrameData) WriteUint64(x uint64) {
	for x >= 0x80 {
		*fd = append(*fd, byte(x)|0x80)
		x >>= 7
	}
	*fd = append(*fd, byte(x))
	return
}

// WriteInt64 writes an int64 to a FrameData using a portable encoding.
func (fd *FrameData) WriteInt64(x int64) {
	if x >= 0 {
		fd.WriteUint64(uint64(x) << 1)
	} else {
		fd.WriteUint64(uint64(-x)<<1 | 1)
	}
	return
}

// WriteLen writes a nonnegative integer less than 0x8000 to a FrameData
// using a portable encoding.
func (fd *FrameData) WriteLen(x int) error {
	switch {
	case x < 0:
		return ErrLengthNegative
	case x < 0x80:
		*fd = append(*fd, byte(x))
	case x < 0x7fff:
		*fd = append(*fd, byte(x>>8)|0x80, byte(x))
	default:
		return ErrLengthOverflow
	}
	return nil
}

// WriteString writes a string to a FrameData. The string must be
// less than 0x8000 bytes long.
func (fd *FrameData) WriteString(s string) {
	if len(s) == 0 {
		*fd = append(*fd, byte(0), byte(1))
		return
	}
	// TODO implement string lookup
	if fd.WriteLen(len(s)) == nil {
		*fd = append(*fd, s...)
	}
	return
}

// WriteByteArrayString writes a byte array that is considered a string
// to a FrameData. The byte array must be less than 0x8000 bytes long.
func (fd *FrameData) WriteByteArrayString(ba []byte) {
	if len(ba) == 0 {
		*fd = append(*fd, byte(0), byte(1))
		return
	}
	// TODO implement string lookup
	if fd.WriteLen(len(ba)) == nil {
		*fd = append(*fd, ba...)
	}
	return
}

// WriteStringNull writes a NULL string to a FrameData. A NULL string
// is used as a marker is the protocol and is distinct from an empty
// string.
func (fd *FrameData) WriteStringNull() {
	*fd = append(*fd, byte(0), byte(0))
	return
}

// WriteByte appends a single byte.
func (fd *FrameData) WriteByte(b byte) error {
	*fd = append(*fd, b)
	return nil
}

// WriteRecordType writes a frame record type constant.
func (fd *FrameData) WriteRecordType(rt RecordType) {
	*fd = append(*fd, byte(rt))
}

// ByteCount returns the number of bytes in the FrameData as an uint64.
func (fd FrameData) ByteCount() uint64 {
	return uint64(len(fd))
}

// ReadFrom reads a FrameData from an io.Reader.
// Implements io.ReaderFrom interface for FrameData.
func (fd *FrameData) ReadFrom(r io.Reader) (n int64, err error) {
	var num int // needed to let ReadFrom/ReadFull integrate well.

	num, err = io.ReadFull(r, (*fd)[:FrameHeaderSize])
	n = int64(num)
	if err == nil && fd.Header().HasPayload() {
		// if below line panics it means we got a frame larger than FrameMaxSize
		*fd = (*fd)[:FrameHeaderSize+int(fd.Header().SizeValue())]
		num, err = io.ReadFull(r, (*fd)[FrameHeaderSize:])
		n += int64(num)
	}

	return
}

// ErrFrameTooBig means a frame with more than FrameMaxPayloadSize bytes occured
var ErrFrameTooBig = errors.New("rap: frame too big")

// WriteTo implements io.WriterTo for FrameData.
func (fd FrameData) WriteTo(w io.Writer) (int64, error) {
	if fd.Header().HasPayload() {
		payloadLength := int32(len(fd)) - FrameHeaderSize
		if payloadLength < 0 {
			log.Fatal("FrameData.WriteTo(): negative payload length")
		}
		if payloadLength > FrameMaxPayloadSize {
			return 0, ErrFrameTooBig
		}
		fd.Header().SetSizeValue(payloadLength)
	}
	// log.Print("FrameData.WriteTo(): ", fd)
	n := 0
	for n < len(fd) {
		m, err := w.Write(fd[n:])
		n += m
		if err != nil {
			return int64(n), err
		}
	}
	return int64(n), nil
}
