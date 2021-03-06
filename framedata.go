// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

package rap

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// ErrLengthNegative is returned for strings with negative length.
type ErrLengthNegative struct{}

func (ErrLengthNegative) Error() string { return "length negative" }

// ErrLengthOverflow is returned for strings longer than 32K.
type ErrLengthOverflow struct{}

func (ErrLengthOverflow) Error() string { return "length overflow" }

// ErrFrameTooBig means a frame with more than FrameMaxPayloadSize bytes occurred
type ErrFrameTooBig struct{}

func (ErrFrameTooBig) Error() string { return "frame too big" }

// ErrFrameTooSmall means a frame size value is smaller than allowed
type ErrFrameTooSmall struct{}

func (ErrFrameTooSmall) Error() string { return "frame too small" }

// ErrInvalidRouteIndex means a route index is less than one or larger than allowed
type ErrInvalidRouteIndex struct{}

func (ErrInvalidRouteIndex) Error() string { return "invalid route index" }

// FrameDataReader is the interface that wraps the ReadFrameData() method.
type FrameDataReader interface {
	ReadFrameData(ConnID) (FrameData, error)
}

// FrameDataWriter is the interface that wraps the WriteFrameData() method.
type FrameDataWriter interface {
	WriteFrameData(ConnID, FrameData) error
}

// FrameData is a byte array used as a network data frame.
type FrameData []byte

// NewFrameData allocates a new FrameData without header.
func NewFrameData() FrameData {
	return FrameData(make([]byte, 0, FrameMaxSize))
}

// NewFrameDataID allocates a new FrameData with a header and ConnID set.
func NewFrameDataID(connID ConnID) (fd FrameData) {
	fd = NewFrameData()
	fd.WriteHeader(connID)
	return
}

// Clear removes everything in a frame
func (fd *FrameData) Clear() {
	*fd = (*fd)[:0]
}

// ClearID removes everything in a frame except the header,
// which is zeroed out and has the ConnID set.
func (fd *FrameData) ClearID(connID ConnID) {
	*fd = (*fd)[:FrameHeaderSize]
	FrameHeader(*fd).ClearID(connID)
}

func (fd FrameData) String() string {
	var contents string
	if fd != nil {
		if len(fd) > 32 {
			contents = hex.EncodeToString(fd[FrameHeaderSize:32]) + "..."
		} else {
			contents = hex.EncodeToString(fd[FrameHeaderSize:])
		}
		return fmt.Sprintf("[FrameData %v %v]", fd.Header(), contents)
	}
	return "[FrameData nil]"
}

// Header returns the FrameHeader part of a FrameData.
func (fd FrameData) Header() FrameHeader {
	return FrameHeader(fd)
}

// Payload returns the payload of a FrameData as a byte slice.
func (fd FrameData) Payload() []byte {
	return fd[FrameHeaderSize:]
}

// SetSizeValue sets the header size value to the current payload size.
func (fd FrameData) SetSizeValue() {
	payloadSize := len(fd) - FrameHeaderSize
	if payloadSize < 0 {
		panic(ErrFrameTooSmall{})
	}
	FrameHeader(fd).SetSizeValue(payloadSize)
}

// Available returns number of free bytes in the FrameData.
func (fd FrameData) Available() int {
	return cap(fd) - len(fd)
}

// Buffered returns the number of bytes that have been written to the
// current frame, including the header size.
func (fd FrameData) Buffered() int {
	return len(fd)
}

// Write implements io.Writer for FrameData, and is used to write body data.
func (fd *FrameData) Write(p []byte) (n int, err error) {
	*fd = append(*fd, p...)
	return len(p), nil
}

// WriteHeader initializes the frame header.
func (fd *FrameData) WriteHeader(connID ConnID) {
	*fd = (*fd)[:FrameHeaderSize]
	FrameHeader(*fd).ClearID(connID)
	return
}

// WriteMuxerControl initializes the frame with the given control code.
func (fd *FrameData) WriteMuxerControl(mc MuxerControl) {
	*fd = (*fd)[:FrameHeaderSize]
	FrameHeader(*fd).SetMuxerControl(mc)
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
	ux := uint64(x) << 1
	if x < 0 {
		ux = ^ux
	}
	fd.WriteUint64(ux)
	return
}

// WriteLen writes a nonnegative integer less than 0x8000 to a FrameData
// using a portable encoding.
func (fd *FrameData) WriteLen(x int) error {
	switch {
	case x < 0:
		return errors.WithStack(ErrLengthNegative{})
	case x < 0x80:
		*fd = append(*fd, byte(x))
	case x <= 0x7fff:
		*fd = append(*fd, byte(x>>8)|0x80, byte(x))
	default:
		return errors.WithStack(ErrLengthOverflow{})
	}
	return nil
}

// WriteString writes a string to a FrameData. The string must be
// less than 0x8000 bytes long.
func (fd *FrameData) WriteString(s string) (err error) {
	if len(s) == 0 {
		*fd = append(*fd, byte(0), byte(1))
		return
	}
	// TODO implement string lookup
	if err = fd.WriteLen(len(s)); err == nil {
		*fd = append(*fd, s...)
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

// WriteRoute writes an unregistered route to a FrameData.
func (fd *FrameData) WriteRoute(s string) (err error) {
	fd.WriteLen(0)
	return fd.WriteString(s)
}

// WriteRegisteredRoute writes a registered route to a FrameData.
func (fd *FrameData) WriteRegisteredRoute(idx int, vals []string) (err error) {
	if idx < 1 {
		return errors.WithStack(ErrInvalidRouteIndex{})
	}
	if err = fd.WriteLen(idx); err == nil {
		for _, s := range vals {
			if err = fd.WriteString(s); err != nil {
				return
			}
		}
	}
	return
}

// WriteByte appends a single byte.
func (fd *FrameData) WriteByte(b byte) error {
	*fd = append(*fd, b)
	return nil
}

// WriteBytes appends a slice of bytes.
func (fd *FrameData) WriteBytes(bs []byte) error {
	*fd = append(*fd, bs...)
	return nil
}

// WriteRecordType writes a frame record type constant.
func (fd *FrameData) WriteRecordType(rt RecordType) {
	fd.Header().SetHead()
	*fd = append(*fd, byte(rt))
}

// ByteCount returns the number of bytes in the FrameData as an uint64.
func (fd FrameData) ByteCount() uint64 {
	return uint64(len(fd))
}

// ReadFrom reads a complete or partial FrameData from an io.Reader.
// Implements io.ReaderFrom interface for FrameData.
func (fd *FrameData) ReadFrom(r io.Reader) (n int64, err error) {
	var num int // needed to let ReadFrom/ReadFull integrate well.

	if len(*fd) < FrameHeaderSize {
		num, err = io.ReadFull(r, (*fd)[len(*fd):FrameHeaderSize])
		*fd = (*fd)[:len(*fd)+num]
		n = int64(num)
		if err != nil || len(*fd) < FrameHeaderSize {
			return
		}
	}
	if fd.Header().HasPayload() {
		endIndex := FrameHeaderSize + int(fd.Header().SizeValue())
		if endIndex < len(*fd) {
			return n, errors.WithStack(ErrFrameTooSmall{})
		}
		if endIndex > FrameMaxSize {
			return n, errors.WithStack(ErrFrameTooBig{})
		}
		num, err = io.ReadFull(r, (*fd)[len(*fd):endIndex])
		*fd = (*fd)[:len(*fd)+num]
		n += int64(num)
	}

	return
}

// WriteTo implements io.WriterTo for FrameData.
func (fd FrameData) WriteTo(w io.Writer) (int64, error) {
	if len(fd) < FrameHeaderSize {
		panic("FrameData.WriteTo(): frame has incomplete header")
	}
	if fd.Header().HasPayload() {
		payloadLength := len(fd) - FrameHeaderSize
		if payloadLength > FrameMaxPayloadSize {
			return 0, errors.WithStack(ErrFrameTooBig{})
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

// WriteRequest writes a FrameTypeRequest record to a FrameData given a http.Request.
func (fd *FrameData) WriteRequest(r *http.Request) error {
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.WriteString(r.Method)
	fd.WriteString(r.URL.Scheme)

	np := path.Clean(strings.Replace(r.URL.Path, "\\", "/", -1))
	if np != "/" && r.URL.Path[len(r.URL.Path)-1] == '/' {
		np += "/"
	}
	fd.WriteRoute(np)

	qv := r.URL.Query()
	for k, vv := range qv {
		fd.WriteString(k)
		for _, v := range vv {
			fd.WriteString(v)
		}
		fd.WriteStringNull()
	}
	fd.WriteStringNull()

	contentLength := r.ContentLength
	haveContentLengthHeader := false
	host := r.Host
	haveHostHeader := false
	for k, vv := range r.Header {
		if !haveContentLengthHeader && k == "Content-Length" {
			haveContentLengthHeader = true
			if contentLength < 0 {
				if n, err := strconv.ParseInt(vv[0], 10, 64); err == nil {
					contentLength = n
				}
			}
		}
		if !haveHostHeader && k == "Host" {
			haveHostHeader = true
			if host == "" {
				host = vv[0]
			}
		}
		fd.WriteString(k)
		for _, v := range vv {
			fd.WriteString(v)
		}
		fd.WriteStringNull()
	}
	fd.WriteStringNull()
	if host == "" {
		fd.WriteStringNull()
	} else {
		fd.WriteString(host)
	}
	if haveContentLengthHeader || contentLength >= 0 {
		fd.WriteInt64(contentLength)
	} else {
		fd.WriteInt64(-1)
	}
	if len(*fd) > FrameMaxSize {
		return errors.WithStack(ErrFrameTooBig{})
	}
	return nil
}

// WriteResponse writes a FrameTypeResponse record to a FrameData given a http.Header.
func (fd *FrameData) WriteResponse(code int, contentLength int64, header http.Header) error {
	// log.Print("FrameData.WriteResponse(", code, ", ", contentLength, ", ", header)
	fd.WriteRecordType(RecordTypeHTTPResponse)
	fd.WriteLen(code)
	for k, vv := range header {
		fd.WriteString(k)
		for _, v := range vv {
			fd.WriteString(v)
		}
		fd.WriteStringNull()
	}
	fd.WriteStringNull()
	fd.WriteInt64(contentLength)
	if len(*fd) > FrameMaxSize {
		return io.ErrShortBuffer
	}
	return nil
}
