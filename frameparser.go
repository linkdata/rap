package rap

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// FrameParser implements reading frame payload from a byte slice
type FrameParser []byte

// NewFrameParser returns a FrameParser from a FrameData
func NewFrameParser(fd FrameData) FrameParser {
	return fd.Payload()
}

func (fp FrameParser) String() string {
	switch {
	case len(fp) < 1:
		return "[FrameReader 0]"
	case len(fp) < 32:
		return fmt.Sprintf("[FrameReader %v %v]", len(fp), hex.EncodeToString(fp))
	default:
		return fmt.Sprintf("[FrameReader %v %v...]", len(fp), hex.EncodeToString(fp[:32]))
	}
}

func (fp *FrameParser) Read(p []byte) (n int, err error) {
	n = copy(p, (*fp))
	(*fp) = (*fp)[n:]
	return
}

// ReadUint64 reads an uint64
func (fp *FrameParser) ReadUint64() (x uint64) {
	var s uint
	for i, b := range *fp {
		if b < 0x80 {
			if i > 9 || i == 9 && b > 1 {
				panic("ReadUint64(): uint64 overflow")
			}
			*fp = (*fp)[i+1:]
			return x | uint64(b)<<s
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	// Did not end with a byte < 0x80
	panic("ReadUint64(): unterminated uint64")
}

// ReadRecordType reads a byte as RecordType
func (fp *FrameParser) ReadRecordType() (rt RecordType) {
	rt = RecordType((*fp)[0])
	(*fp) = (*fp)[1:]
	return
}

// ReadInt64 reads an int64
func (fp *FrameParser) ReadInt64() (x int64) {
	ux := fp.ReadUint64()
	ix := int64(ux >> 1)
	if (ux & 1) != 0 {
		ix = ^ix
	}
	return ix
}

// ReadLen reads a RAP length value
func (fp *FrameParser) ReadLen() (n int) {
	n = int((*fp)[0])
	if n < 0x80 {
		(*fp) = (*fp)[1:]
	} else {
		n = (n&0x7f)<<8 | int((*fp)[1])
		(*fp) = (*fp)[2:]
	}
	return
}

// ReadString reads a RAP string
func (fp *FrameParser) ReadString() (s string, isNull bool) {
	n := fp.ReadLen()
	if n < 1 {
		code := fp.ReadLen()
		if code > 1 {
			// TODO: string table lookup
			panic("unmapped string index")
		}
		s = ""
		if code < 1 {
			isNull = true
		}
	} else {
		s = string((*fp)[:n])
		(*fp) = (*fp)[n:]
	}
	return
}

var zeroHeaderValue = []string{"0"}

// ProxyBody sends the remaining data from the FrameReader to a io.Writer
func (fp *FrameParser) ProxyBody(w io.Writer) (err error) {
	// log.Printf("FrameReader.ProxyBody() %v", hex.EncodeToString(*fr))
	n, err := w.Write((*fp))
	(*fp) = (*fp)[n:]
	// log.Printf("FrameReader.ProxyBody() remainder %v", hex.EncodeToString(*fr))
	return
}

// ReadRequest reads a request structure from a FrameReader and returns a http.Request.
func (fp *FrameParser) ReadRequest() (req *http.Request, err error) {
	methodString, _ := fp.ReadString()
	urlString, _ := fp.ReadString()

	u, err := url.Parse(urlString)
	if err != nil {
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
		RequestURI: urlString,
	}

	if queryKey, isNull := fp.ReadString(); !isNull {
		queryValues := url.Values{}
		for {
			// log.Print("FrameReader.ReadRequest() queryKey ", queryKey)
			for {
				queryValue, isNull := fp.ReadString()
				if isNull {
					break
				}
				// log.Print("FrameReader.ReadRequest() queryKey '", queryKey, "' queryValue '", queryValue, "'")
				queryValues.Add(queryKey, queryValue)
			}
			queryKey, isNull = fp.ReadString()
			if isNull {
				break
			}
		}
		u.RawQuery = queryValues.Encode()
	}

	for {
		headerKey, isNull := fp.ReadString()
		if isNull {
			break
		}
		// log.Print("FrameReader.ReadRequest() headerKey ", headerKey)
		for {
			headerValue, isNull := fp.ReadString()
			if isNull {
				break
			}
			req.Header.Add(headerKey, headerValue)
		}
	}
	if host, isNull := fp.ReadString(); !isNull {
		req.Host = host
	}
	req.ContentLength = fp.ReadInt64()
	if req.ContentLength == 0 {
		req.Header["Content-Length"] = zeroHeaderValue
	} else if req.ContentLength > 0 {
		req.Header["Content-Length"] = []string{strconv.FormatInt(req.ContentLength, 10)}
	}
	return
}

// ProxyResponse reads a RAP response record and writes it to a http.ResponseWriter
func (fp *FrameParser) ProxyResponse(w http.ResponseWriter) {
	header := w.Header()
	statusCode := fp.ReadLen()
	hasCL := false

	for {
		headerKey, isNull := fp.ReadString()
		if isNull {
			break
		}
		if !hasCL && headerKey == "Content-Length" {
			hasCL = true
		}
		for {
			headerValue, isNull := fp.ReadString()
			if isNull {
				break
			}
			header.Add(headerKey, headerValue)
		}
	}

	if contentLength := fp.ReadInt64(); contentLength >= 0 && !hasCL {
		if contentLength == 0 {
			header["Content-Length"] = zeroHeaderValue
		} else {
			header["Content-Length"] = []string{strconv.FormatInt(contentLength, 10)}
		}
	}

	w.WriteHeader(statusCode)
	return
}
