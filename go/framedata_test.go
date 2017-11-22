package rap

import (
	"bytes"
	"io"
	"io/ioutil"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewFrameData(t *testing.T) {
	fd := NewFrameData()
	assert.NotNil(t, fd)
	assert.Equal(t, FrameHeaderSize, fd.Buffered())
	assert.Equal(t, FrameMaxSize-FrameHeaderSize, fd.Available())
}

func TestNewFrameDataExchangeIDRange(t *testing.T) {
	fd := NewFrameData()
	assert.Equal(t, ExchangeID(0), fd.Header().ExchangeID())
	fd.WriteHeader(ExchangeID(1))
	assert.Equal(t, ExchangeID(1), fd.Header().ExchangeID())
	fd.WriteHeader(MaxExchangeID)
	assert.Equal(t, MaxExchangeID, fd.Header().ExchangeID())
	assert.Panics(t, func() { fd.WriteHeader(ExchangeID(0xFFFF)) })
}

func TestFrameDataString(t *testing.T) {
	fd := NewFrameData()
	fd.WriteString("Hello world")
	assert.Equal(t, "[FrameData [FrameHeader [ExchangeID 0000] ... 0 (16)] 0b48656c6c6f20776f726c64]", fd.String())
	fd.WriteString("the data is greater than 32 length")
	assert.Equal(t, "[FrameData [FrameHeader [ExchangeID 0000] ... 0 (51)] 0b48656c6c6f20776f726c6422746865206461746120697320677265...]", fd.String())
}

type shortWriter struct {
	w io.Writer
	n int64
}

func (t *shortWriter) Write(p []byte) (n int, err error) {
	if t.n <= 0 {
		return 0, io.ErrShortWrite
	}
	// real write
	n = len(p)
	if int64(n) > t.n {
		n = int(t.n)
	}
	n, err = t.w.Write(p[0:n])
	t.n -= int64(n)
	return
}

func TestFrameDataPayloadAndWriteTo(t *testing.T) {
	fd := NewFrameData()
	assert.NotNil(t, fd.Payload())
	assert.Equal(t, 0, len(fd.Payload()))
	ba := make([]byte, 0, FrameMaxPayloadSize)
	assert.Panics(t, func() { FrameData(ba).WriteTo(ioutil.Discard) })
	for i := 0; i < FrameMaxPayloadSize; i++ {
		b := byte(i % 0xff)
		fd.WriteByte(b)
		ba = append(ba, b)
	}
	assert.Equal(t, ba, fd.Payload())
	fd.Header().SetBody()

	_, err := fd.WriteTo(&shortWriter{ioutil.Discard, 1})
	assert.Equal(t, io.ErrShortWrite, err)

	_, err = fd.WriteTo(ioutil.Discard)
	assert.NoError(t, err)
	fd.WriteByte(0x00)

	_, err = fd.WriteTo(ioutil.Discard)
	assert.Equal(t, ErrFrameTooBig, err)
}

func TestFrameDataWriteUint64(t *testing.T) {
	// Test encodings
	for i := uint(0); i < 64; i++ {
		for j := uint64(0); j < 3; j++ {
			n := ((uint64(1) << i) - 1) + j
			fd := NewFrameData()
			fd.WriteUint64(n)
			fr := NewFrameReader(fd)
			assert.Equal(t, n, fr.ReadUint64())
		}
	}
	// Test unterminated
	fd := NewFrameData()
	for i := 0; i < 11; i++ {
		fd.WriteByte(0xff)
	}
	fr := NewFrameReader(fd)
	assert.Panics(t, func() { fr.ReadUint64() })
	// Test overflow
	fd.WriteByte(0x00)
	fr = NewFrameReader(fd)
	assert.Panics(t, func() { fr.ReadUint64() })
}

func TestFrameDataWriteInt64(t *testing.T) {
	// Test encodings
	for i := uint(0); i < 64; i++ {
		for j := int64(0); j < 3; j++ {
			for k := int64(-1); k < 2; k += 2 {
				n := (((int64(1) << i) - 1) + j) * k
				fd := NewFrameData()
				fd.WriteInt64(n)
				fr := NewFrameReader(fd)
				assert.Equal(t, n, fr.ReadInt64())
			}
		}
	}
}

func TestFrameDataWriteLen(t *testing.T) {
	for i := uint(0); i < 16; i++ {
		for j := int(-1); j < 3; j++ {
			n := (((1 << i) - 1) + j)
			fd := NewFrameData()
			err := fd.WriteLen(n)
			if n < 0 || n > 0x7fff {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				fr := NewFrameReader(fd)
				assert.Equal(t, n, fr.ReadLen())
			}
		}
	}
}

func TestFrameDataWriteStringNull(t *testing.T) {
	fd := NewFrameData()
	fd.WriteStringNull()
	fr := NewFrameReader(fd)
	s, isNull := fr.ReadString()
	assert.True(t, isNull)
	assert.Equal(t, "", s)
}

func TestFrameDataWriteStringEmpty(t *testing.T) {
	fd := NewFrameData()
	fd.WriteString("")
	fr := NewFrameReader(fd)
	s, isNull := fr.ReadString()
	assert.False(t, isNull)
	assert.Equal(t, "", s)
}

func TestFrameDataWriteStringAscii(t *testing.T) {
	const helloWorld = "Hello world"
	fd := NewFrameData()
	fd.WriteString(helloWorld)
	fr := NewFrameReader(fd)
	s, isNull := fr.ReadString()
	assert.False(t, isNull)
	assert.Equal(t, helloWorld, s)
}

func TestFrameDataWriteStringUnicode(t *testing.T) {
	const helloWorld = "Hello world Åäö!"
	fd := NewFrameData()
	fd.WriteString(helloWorld)
	fr := NewFrameReader(fd)
	s, isNull := fr.ReadString()
	assert.False(t, isNull)
	assert.Equal(t, helloWorld, s)
}

func TestFrameDataWriteStringOverflow(t *testing.T) {
	var buffer bytes.Buffer
	for j := 0; j < 0x8002; j++ {
		buffer.WriteString("a")
		if j > 0x7ffd {
			expected := buffer.String()
			fd := NewFrameData()
			err := fd.WriteString(expected)
			if len(expected) < 0x8000 {
				assert.NoError(t, err)
				fr := NewFrameReader(fd)
				s, isNull := fr.ReadString()
				assert.False(t, isNull)
				assert.Equal(t, expected, s)
			} else {
				assert.Error(t, err)
			}
		}
	}
}

func TestFrameDataWriteRecordTypeAndByteCount(t *testing.T) {
	fd := NewFrameData()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	assert.Equal(t, uint64(FrameHeaderSize+1), fd.ByteCount())
}

func TestFrameDataReadFrom(t *testing.T) {
	fd1 := NewFrameData()
	fd1.WriteHeader(0x1234)
	fd1.Header().SetBody()
	fd1.WriteByte(0)
	fd1.WriteInt64(-0x123456789)
	fd1.WriteLen(0x12)
	fd1.WriteRecordType(RecordTypeUserFirst)
	fd1.WriteStringNull()
	fd1.WriteString("")
	fd1.WriteString("Hello world")
	fd1.WriteUint64(0x123456789)
	r, w := io.Pipe()
	var n1, n2 int64
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(1)
	go func(pn1 *int64) {
		defer w.Close()
		*pn1, err1 = fd1.WriteTo(w)
		assert.NoError(t, err1)
		assert.NotZero(t, n1)
		wg.Done()
	}(&n1)
	fd2 := NewFrameData()
	n2, err2 = fd2.ReadFrom(r)
	wg.Wait()
	assert.NoError(t, err2)
	assert.Equal(t, n1, n2)
	assert.Equal(t, fd1.Payload(), fd2.Payload())
}
