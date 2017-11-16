package rap

import (
	"encoding/hex"
	"fmt"
	"io"
)

// FrameReader implements reading frame payload from a byte slice
type FrameReader []byte

// NewFrameReader returns a FrameReader from a FrameData
func NewFrameReader(fd FrameData) FrameReader {
	return fd.Payload()
}

func (fr FrameReader) String() string {
	return fmt.Sprintf("[FrameReader %v %v]", len(fr), hex.EncodeToString(fr))
}

func (fr *FrameReader) Read(p []byte) (n int, err error) {
	n = copy(p, (*fr))
	(*fr) = (*fr)[n:]
	return
}

// ReadUint64 reads an uint64
func (fr *FrameReader) ReadUint64() (x uint64) {
	var s uint
	for i, b := range *fr {
		if b < 0x80 {
			if i > 9 || i == 9 && b > 1 {
				// overflow
				return 0
			}
			*fr = (*fr)[i+1:]
			return x | uint64(b)<<s
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	return 0 //, 0
}

// ReadUint32 reads an uint32
func (fr *FrameReader) ReadUint32() (x uint32) {
	x = uint32((*fr)[0])<<24 | uint32((*fr)[1])<<16 | uint32((*fr)[2])<<8 | uint32((*fr)[3])
	(*fr) = (*fr)[4:]
	return x
}

// ReadUint16 reads an uint16
func (fr *FrameReader) ReadUint16() (x uint16) {
	x = uint16((*fr)[0])<<8 | uint16((*fr)[1])
	(*fr) = (*fr)[2:]
	return x
}

// ReadByte reads an byte
func (fr *FrameReader) ReadByte() (x byte, err error) {
	x = (*fr)[0]
	(*fr) = (*fr)[1:]
	return x, nil
}

// ReadRecordType reads a byte as RecordType
func (fr *FrameReader) ReadRecordType() (rt RecordType) {
	rt = RecordType((*fr)[0])
	(*fr) = (*fr)[1:]
	return
}

// ReadInt64 reads an int64
func (fr *FrameReader) ReadInt64() (x int64) {
	ux := fr.ReadUint64()
	ix := int64(ux >> 1)
	if (ux & 1) == 1 {
		return -ix
	}
	return ix
}

// ReadLen reads a RAP length value
func (fr *FrameReader) ReadLen() (n int) {
	n = int((*fr)[0])
	if n < 0x80 {
		(*fr) = (*fr)[1:]
	} else {
		n = (n&0x7f)<<8 | int((*fr)[1])
		(*fr) = (*fr)[2:]
	}
	return
}

// ReadString reads a RAP string
func (fr *FrameReader) ReadString() (s string, isNull bool) {
	n := fr.ReadLen()
	if n < 1 {
		code := (*fr)[0]
		(*fr) = (*fr)[1:]
		if code < 2 {
			s = ""
			if code < 1 {
				isNull = true
			}
		} else {
			s = ""
		}
	} else {
		s = string((*fr)[:n])
		(*fr) = (*fr)[n:]
	}
	return
}

var zeroHeaderValue = []string{"0"}

// ProxyBody sends the body data from the reader to a io.Writer
func (fr *FrameReader) ProxyBody(w io.Writer) (err error) {
	// log.Printf("FrameReader.ProxyBody() %v", hex.EncodeToString(*fr))
	bodySize := len(*fr)
	// log.Printf("FrameReader.ProxyBody() bodySize %v is %v", bodySize, hex.EncodeToString((*fr)[:bodySize]))
	n, err := w.Write((*fr)[:bodySize])
	(*fr) = (*fr)[n:]
	// log.Printf("FrameReader.ProxyBody() remainder %v", hex.EncodeToString(*fr))
	if n != bodySize {
		err = io.ErrUnexpectedEOF
	}
	return
}

