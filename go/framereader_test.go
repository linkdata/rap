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
	fr := NewFrameReader(fd)
	assert.Equal(t, "[FrameReader 0]", fr.String())
	fd.WriteByte(0x01)
	fr = NewFrameReader(fd)
	assert.Equal(t, "[FrameReader 1 01]", fr.String())
	fd.WriteString("Hello world with a longer string")
	fr = NewFrameReader(fd)
	assert.Equal(t, "[FrameReader 34 012048656c6c6f20776f726c6420776974682061206c6f6e6765722073747269...]", fr.String())
}

func Test_FrameReader_Read(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteString("quuxFooBAR")
	fr := NewFrameReader(fd)
	ba := make([]byte, 11)
	fr.Read(ba)
	assert.Equal(t, []byte{0xa, 0x71, 0x75, 0x75, 0x78, 0x46, 0x6f, 0x6f, 0x42, 0x41, 0x52}, ba)
}

func Test_FrameReader_ReadRequest_IllegalURL(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteStringNull()  // method
	fd.WriteString(":a:") // illegal url
	fr := NewFrameReader(fd)
	req, err := fr.ReadRequest()
	assert.Nil(t, req)
	assert.Error(t, err)
}

func Test_FrameReader_ProxyResponse(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0x1234)
	fd.WriteResponse(200, 0, nil)
	fr := NewFrameReader(fd)
	rr := &httptest.ResponseRecorder{}
	assert.Equal(t, RecordTypeHTTPResponse, fr.ReadRecordType())
	fr.ProxyResponse(rr)

	fd.Clear()
	fd.WriteHeader(0x0123)
	h := http.Header{}
	h.Add("Status", "Meh")
	h.Add("Foo", "bar")
	h.Add("Foo", "quux")
	err := fd.WriteResponse(300, 234, h)
	assert.NoError(t, err)
	rr = &httptest.ResponseRecorder{}
	fr = NewFrameReader(fd)
	assert.Equal(t, RecordTypeHTTPResponse, fr.ReadRecordType())
	fr.ProxyResponse(rr)
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
	fr = NewFrameReader(fd)
	assert.Equal(t, RecordTypeHTTPResponse, fr.ReadRecordType())
	fr.ProxyResponse(rr)
	assert.Equal(t, 200, rr.Code)
	assert.Equal(t, "123", rr.Header().Get("Content-Length"))
}
