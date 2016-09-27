package rap

import (
	"net/http"
	"path"
	"strconv"
	"strings"
    "io"
)

// WriteRequest writes a FrameTypeRequest record to a FrameData given a http.Request.
func (fd *FrameData) WriteRequest(r *http.Request) error {
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.WriteString(r.Method)

	np := path.Clean(strings.Replace(r.URL.Path, "\\", "/", -1))
	if np != "/" && r.URL.Path[len(r.URL.Path)-1] == '/' {
		np += "/"
	}
	fd.WriteString(np)

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
			continue
		}
		if !haveHostHeader && k == "Host" {
			haveHostHeader = true
			if host == "" {
				host = vv[0]
			}
			continue
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
	if haveContentLengthHeader || contentLength > 0 {
		fd.WriteInt64(contentLength)
	} else {
		fd.WriteInt64(-1)
	}
	if len(*fd) > FrameMaxSize {
		return ErrFrameTooBig
	}
	return nil
}

// WriteResponse writes a FrameTypeResponse record to a FrameData given a http.Header.
func (fd *FrameData) WriteResponse(code int, contentLength int64, header http.Header) error {
	// log.Print("FrameData.WriteResponse(", code, ", ", contentLength, ", ", header)
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPResponse)
	fd.WriteUint16(uint16(code))
	statusText := ""
	for k, vv := range header {
		if k == "Status" {
			statusText = vv[0]
			continue
		}
		fd.WriteString(k)
		for _, v := range vv {
			fd.WriteString(v)
		}
		fd.WriteStringNull()
	}
	fd.WriteStringNull()
	if statusText == "" {
		fd.WriteStringNull()
	} else {
		fd.WriteString(statusText)
	}
	fd.WriteInt64(contentLength)
	if len(*fd) > FrameMaxSize {
		return io.ErrShortBuffer
	}
	return nil
}
