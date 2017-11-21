package rap

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
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
				panic("ReadUint64(): uint64 overflow")
			}
			*fr = (*fr)[i+1:]
			return x | uint64(b)<<s
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	// Did not end with a byte < 0x80
	panic("ReadUint64(): unterminated uint64")
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
	if (ux & 1) != 0 {
		ix = ^ix
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
		code := fr.ReadLen()
		if code > 1 {
			// TODO: string table lookup
		}
		s = ""
		if code < 1 {
			isNull = true
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

// ReadRequest reads a request structure from a FrameReader and returns a http.Request.
func (fr *FrameReader) ReadRequest() (req *http.Request, err error) {
	methodString, _ := fr.ReadString()
	urlString, _ := fr.ReadString()

	u, err := url.Parse(urlString)
	if err != nil {
		log.Fatal("FrameReader.ReadRequest(): ", err.Error())
		return
	}
	u.Scheme = "http"

	req = &http.Request{
		Method:     methodString,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Close:      false,
	}

	for {
		queryKey, isNull := fr.ReadString()
		if isNull {
			break
		}
		// log.Print("FrameReader.ReadRequest() queryKey ", queryKey)
		for {
			queryValue, isNull := fr.ReadString()
			if isNull {
				break
			}
			// log.Print("FrameReader.ReadRequest() queryKey '", queryKey, "' queryValue '", queryValue, "'")
			u.Query().Add(queryKey, queryValue)
		}
	}

	for {
		headerKey, isNull := fr.ReadString()
		if isNull {
			break
		}
		/*
			// RAP request records headers may not contain Host or Content-Length
			if headerKey == "Host" {
				panic("FrameReader.ReadRequest(): Host in headers")
			}
			if headerKey == "Content-Length" {
				panic("FrameReader.ReadRequest(): Content-Length in headers")
			}
		*/
		// log.Print("FrameReader.ReadRequest() headerKey ", headerKey)
		for {
			headerValue, isNull := fr.ReadString()
			if isNull {
				break
			}
			req.Header.Add(headerKey, headerValue)
		}
	}
	if host, isNull := fr.ReadString(); !isNull {
		u.Host = host
		req.Header["Host"] = []string{host}
	}
	req.ContentLength = fr.ReadInt64()
	if req.ContentLength == 0 {
		req.Header["Content-Length"] = zeroHeaderValue
	} else if req.ContentLength > 0 {
		req.Header["Content-Length"] = []string{strconv.FormatInt(req.ContentLength, 10)}
	}
	return
}

// ProxyResponse reads a RAP response record and writes it to a http.ResponseWriter
func (fr *FrameReader) ProxyResponse(w http.ResponseWriter) {
	header := w.Header()
	statusCode := int(fr.ReadUint16())
	hasCL := false
	hasStatus := false

	for {
		headerKey, isNull := fr.ReadString()
		if isNull {
			break
		}
		if !hasCL && headerKey == "Content-Length" {
			hasCL = true
		}
		if !hasStatus && headerKey == "Status" {
			hasStatus = true
		}
		for {
			headerValue, isNull := fr.ReadString()
			if isNull {
				break
			}
			header.Add(headerKey, headerValue)
		}
	}

	if statusText, isNull := fr.ReadString(); !isNull && !hasStatus {
		header["Status"] = []string{statusText}
	}

	if contentLength := fr.ReadInt64(); contentLength >= 0 && !hasCL {
		if contentLength == 0 {
			header["Content-Length"] = zeroHeaderValue
		} else {
			header["Content-Length"] = []string{strconv.FormatInt(contentLength, 10)}
		}
	}

	w.WriteHeader(statusCode)
	return
}
