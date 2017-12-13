package rap

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_FrameReader_String(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fp := NewFrameParser(fd)
	assert.Equal(t, "[FrameReader 0]", fp.String())
	fd.WriteByte(0x01)
	fp = NewFrameParser(fd)
	assert.Equal(t, "[FrameReader 1 01]", fp.String())
	fd.WriteString("Hello world with a longer string")
	fp = NewFrameParser(fd)
	assert.Equal(t, "[FrameReader 34 012048656c6c6f20776f726c6420776974682061206c6f6e6765722073747269...]", fp.String())
}

func Test_FrameReader_Read(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteString("quuxFooBAR")
	fp := NewFrameParser(fd)
	ba := make([]byte, 11)
	fp.Read(ba)
	assert.Equal(t, []byte{0xa, 0x71, 0x75, 0x75, 0x78, 0x46, 0x6f, 0x6f, 0x42, 0x41, 0x52}, ba)
}

func Test_FrameReader_ReadRequest_IllegalURL(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteStringNull()  // method
	fd.WriteString(":a:") // illegal url
	fp := NewFrameParser(fd)
	req, err := fp.ReadRequest()
	assert.Nil(t, req)
	assert.Error(t, err)
}

func Test_FrameReader_ProxyResponse(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0x123)
	fd.WriteResponse(200, 0, nil)
	fp := NewFrameParser(fd)
	rr := &httptest.ResponseRecorder{}
	assert.Equal(t, RecordTypeHTTPResponse, fp.ReadRecordType())
	fp.ProxyResponse(rr)

	fd.Clear()
	fd.WriteHeader(0x0123)
	h := http.Header{}
	h.Add("Status", "Meh")
	h.Add("Foo", "bar")
	h.Add("Foo", "quux")
	err := fd.WriteResponse(300, 234, h)
	assert.NoError(t, err)
	rr = &httptest.ResponseRecorder{}
	fp = NewFrameParser(fd)
	assert.Equal(t, RecordTypeHTTPResponse, fp.ReadRecordType())
	fp.ProxyResponse(rr)
	assert.Equal(t, 300, rr.Code)
	assert.Equal(t, "234", rr.Header().Get("Content-Length"))

	fd.Clear()
	fd.WriteHeader(0x0123)
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPResponse)
	fd.WriteLen(200)
	fd.WriteString("Content-Length")
	fd.WriteString("123")
	fd.WriteStringNull()
	fd.WriteStringNull()
	fd.WriteInt64(-1)
	rr = &httptest.ResponseRecorder{}
	fp = NewFrameParser(fd)
	assert.Equal(t, RecordTypeHTTPResponse, fp.ReadRecordType())
	fp.ProxyResponse(rr)
	assert.Equal(t, 200, rr.Code)
	assert.Equal(t, "123", rr.Header().Get("Content-Length"))
}
