package rap

import (
	"net/http"
	"strconv"
)

// ResponseWriter implements http.ResponseWriter for a Conn
type ResponseWriter struct {
	*Conn
	Code        int         // the HTTP response code from WriteHeader
	HeaderMap   http.Header // the HTTP response headers
	Flushed     bool
	wroteHeader bool
}

// NewResponseWriter returns an initialized ResponseRecorder.
func NewResponseWriter(e *Conn) *ResponseWriter {
	return &ResponseWriter{
		Conn:      e,
		HeaderMap: make(http.Header),
		Code:      200,
	}
}

// Header returns the response headers.
func (rw *ResponseWriter) Header() http.Header {
	m := rw.HeaderMap
	if m == nil {
		m = make(http.Header)
		rw.HeaderMap = m
	}
	return m
}

// Write always succeeds and writes to rw.Body, if not nil.
func (rw *ResponseWriter) Write(buf []byte) (int, error) {
	// log.Print("ResponseWriter.Write([", len(buf), "]byte) ", rw.Conn)
	if !rw.wroteHeader {
		rw.WriteHeader(200)
	}
	return rw.Conn.Write(buf)
}

// WriteHeader sets rw.Code.
func (rw *ResponseWriter) WriteHeader(code int) {
	// log.Print("ResponseWriter.WriteHeader(", code, ") ", rw.Conn)
	if !rw.wroteHeader {
		rw.Code = code
		contentLength := int64(-1)
		if rw.HeaderMap != nil {
			if cl, ok := rw.HeaderMap["Content-Length"]; ok {
				var err error
				if contentLength, err = strconv.ParseInt(cl[0], 10, 64); err != nil {
					contentLength = -1
				}
			}
		}
		rw.Conn.WriteResponseData(rw.Code, contentLength, rw.HeaderMap)
		rw.wroteHeader = true
	}
}

// Reset sets the ResponseWriter to the initial state.
func (rw *ResponseWriter) Reset() {
	rw.Code = 200
	rw.HeaderMap = nil
	rw.Flushed = false
	rw.wroteHeader = false
}

// Flush sets rw.Flushed to true.
func (rw *ResponseWriter) Flush() {
	if !rw.Flushed {
		if !rw.wroteHeader {
			rw.WriteHeader(200)
		}
		rw.Conn.Close()
		rw.Flushed = true
	}
}
