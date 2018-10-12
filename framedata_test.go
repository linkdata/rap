package rap

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func Test_NewFrameData(t *testing.T) {
	fd := NewFrameData()
	assert.NotNil(t, fd)
	assert.Equal(t, 0, fd.Buffered())
	fd.WriteHeader(0)
	assert.Equal(t, FrameHeaderSize, fd.Buffered())
	assert.Equal(t, FrameMaxSize-FrameHeaderSize, fd.Available())
}

func Test_NewFrameData_exchange_ID_range(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(ExchangeID(1))
	assert.Equal(t, ExchangeID(1), fd.Header().ExchangeID())
	fd.WriteHeader(MaxExchangeID)
	assert.Equal(t, MaxExchangeID, fd.Header().ExchangeID())
	assert.Panics(t, func() { fd.WriteHeader(ExchangeID(0xFFFF)) })
}

func Test_FrameData_String(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteString("Hello world")
	assert.Equal(t, "[FrameData [FrameHeader [ID 0000] ... 0 (16)] 0b48656c6c6f20776f726c64]", fd.String())
	fd.WriteString("the data is greater than 32 length")
	assert.Equal(t, "[FrameData [FrameHeader [ID 0000] ... 0 (51)] 0b48656c6c6f20776f726c6422746865206461746120697320677265...]", fd.String())
	fd = nil
	assert.Equal(t, "[FrameData nil]", fd.String())
}

func Test_FrameData_WriteRoute(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	assert.NoError(t, fd.WriteRoute("/"))
	assert.Equal(t, "[FrameData [FrameHeader [ID 0000] ... 0 (7)] 00012f]", fd.String())
}

func Test_FrameData_WriteRegisteredRoute(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	assert.NoError(t, fd.WriteRegisteredRoute(1, []string{"foo", "bar"}))
	assert.Equal(t, "[FrameData [FrameHeader [ID 0000] ... 0 (13)] 0103666f6f03626172]", fd.String())
	assert.Equal(t, ErrInvalidRouteIndex{}.Error(), fd.WriteRegisteredRoute(0, []string{}).Error())
}

// WriteRegisteredRoute

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

func Test_FrameData_Write(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.Write([]byte{0x01})
	fr := NewFrameParser(fd)
	ba := make([]byte, 1)
	n, err := fr.Read(ba)
	assert.NoError(t, err)
	assert.Equal(t, 1, n)
}

func Test_FrameData_WriteTo(t *testing.T) {
	fd := NewFrameDataID(0)
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
	assert.Equal(t, ErrFrameTooBig{}, errors.Cause(err))
}

func Test_FrameData_WriteUint64(t *testing.T) {
	// Test encodings
	for i := uint(0); i < 64; i++ {
		for j := uint64(0); j < 3; j++ {
			n := ((uint64(1) << i) - 1) + j
			fd := NewFrameData()
			fd.WriteHeader(0)
			fd.WriteUint64(n)
			fr := NewFrameParser(fd)
			assert.Equal(t, n, fr.ReadUint64())
		}
	}
	// Test unterminated
	fd := NewFrameData()
	fd.WriteHeader(0)
	for i := 0; i < 11; i++ {
		fd.WriteByte(0xff)
	}
	fr := NewFrameParser(fd)
	assert.Panics(t, func() { fr.ReadUint64() })
	// Test overflow
	fd.WriteByte(0x00)
	fr = NewFrameParser(fd)
	assert.Panics(t, func() { fr.ReadUint64() })
}

func Test_FrameData_WriteInt64(t *testing.T) {
	// Test encodings
	for i := uint(0); i < 64; i++ {
		for j := int64(0); j < 3; j++ {
			for k := int64(-1); k < 2; k += 2 {
				n := (((int64(1) << i) - 1) + j) * k
				fd := NewFrameData()
				fd.WriteHeader(0)
				fd.WriteInt64(n)
				fr := NewFrameParser(fd)
				assert.Equal(t, n, fr.ReadInt64())
			}
		}
	}
}

func Test_FrameData_WriteLen(t *testing.T) {
	for i := uint(0); i < 16; i++ {
		for j := int(-1); j < 3; j++ {
			n := (((1 << i) - 1) + j)
			fd := NewFrameData()
			fd.WriteHeader(0)
			err := fd.WriteLen(n)
			if n < 0 || n > 0x7fff {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				fr := NewFrameParser(fd)
				assert.Equal(t, n, fr.ReadLen())
			}
		}
	}
}

func Test_FrameData_WriteLen_errors(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	assert.Equal(t, ErrLengthNegative{}.Error(), fd.WriteLen(-1).Error())
	assert.Equal(t, ErrLengthOverflow{}.Error(), fd.WriteLen(0x8000).Error())
}

func Test_FrameData_WriteStringNull(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteStringNull()
	fr := NewFrameParser(fd)
	s, isNull := fr.ReadString()
	assert.True(t, isNull)
	assert.Equal(t, "", s)
}

func Test_FrameData_WriteStringEmpty(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteString("")
	fr := NewFrameParser(fd)
	s, isNull := fr.ReadString()
	assert.False(t, isNull)
	assert.Equal(t, "", s)
}

func Test_FrameData_WriteString(t *testing.T) {
	const helloWorld = "Hello world Åäö!"
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteString(helloWorld)
	fr := NewFrameParser(fd)
	s, isNull := fr.ReadString()
	assert.False(t, isNull)
	assert.Equal(t, helloWorld, s)
}

func Test_FrameData_WriteString_Overflow(t *testing.T) {
	var buffer bytes.Buffer
	for j := 0; j < 0x8002; j++ {
		buffer.WriteString("a")
		if j > 0x7ffd {
			expected := buffer.String()
			fd := NewFrameData()
			fd.WriteHeader(0)
			err := fd.WriteString(expected)
			if len(expected) < 0x8000 {
				assert.NoError(t, err)
				fr := NewFrameParser(fd)
				s, isNull := fr.ReadString()
				assert.False(t, isNull)
				assert.Equal(t, expected, s)
			} else {
				assert.Error(t, err)
			}
		}
	}
}

func Test_FrameData_WriteRecordType(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(0)
	fd.WriteRecordType(RecordTypeHTTPRequest)
	assert.Equal(t, uint64(FrameHeaderSize+1), fd.ByteCount())
}

func pipeFrame(t *testing.T, fd1 FrameData) (fd2 FrameData, err error) {
	r, w := io.Pipe()
	defer r.Close()
	var n1, n2 int64
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(1)
	go func(pn1 *int64) {
		defer w.Close()
		*pn1, err1 = fd1.WriteTo(w)
		if err1 == nil {
			assert.NotZero(t, n1)
		} else {
			err = err1
		}
		wg.Done()
	}(&n1)
	fd2 = NewFrameData()
	n2, err2 = fd2.ReadFrom(r)
	wg.Wait()
	if err2 == nil {
		assert.Equal(t, n1, n2)
		assert.Equal(t, fd1.Payload(), fd2.Payload())
	} else if err == nil {
		err = err2
	}
	return
}

func Test_FrameData_ReadFrom(t *testing.T) {
	fd1 := NewFrameData()
	fd1.WriteHeader(MaxExchangeID)
	fd1.Header().SetBody()
	fd1.WriteByte(0)
	fd1.WriteInt64(-0x123456789)
	fd1.WriteLen(0x12)
	fd1.WriteRecordType(RecordTypeUserFirst)
	fd1.WriteStringNull()
	fd1.WriteString("")
	fd1.WriteString("Hello world")
	fd1.WriteUint64(0x123456789)
	pipeFrame(t, fd1)

	// Test underflow
	fd1 = NewFrameDataID(MaxExchangeID)
	fd1.Header().SetBody()
	fd1.Header().SetSizeValue(0)
	fd1.WriteString("Meh")
	n, err := fd1.ReadFrom(bytes.NewBuffer([]byte{0x01}))
	assert.Equal(t, ErrFrameTooSmall{}.Error(), err.Error())
	assert.Zero(t, n)

	// Test overflow
	fd1 = NewFrameDataID(MaxExchangeID)
	fd1.Header().SetBody()
	fd1.Header().SetSizeValue(FrameMaxPayloadSize + 1)
	n, err = fd1.ReadFrom(bytes.NewBuffer(make([]byte, FrameMaxPayloadSize+1)))
	assert.Equal(t, ErrFrameTooBig{}.Error(), err.Error())
	assert.Zero(t, n)
}

func pipeRequest(t *testing.T, req *http.Request, checkEqual bool) (req2 *http.Request, err error) {
	var fd2 FrameData
	fd1 := NewFrameData()
	fd1.WriteHeader(MaxExchangeID)
	fd1.WriteRequest(req)
	if req.Body != nil {
		var bodyCopy bytes.Buffer
		bodyBytes, err := ioutil.ReadAll(io.TeeReader(req.Body, &bodyCopy))
		if err != nil {
			return nil, err
		}
		fd1.Header().SetBody()
		fd1.WriteBytes(bodyBytes)
		req.Body = ioutil.NopCloser(&bodyCopy)
	}
	fd2, err = pipeFrame(t, fd1)
	if err != nil {
		return
	}
	fr := NewFrameParser(fd2)
	assert.Equal(t, RecordTypeHTTPRequest, fr.ReadRecordType())
	req2, err = fr.ReadRequest()
	if err == nil {
		assert.NotNil(t, req2)
		if fd2.Header().HasBody() {
			var bodyBuffer bytes.Buffer
			err = fr.ProxyBody(&bodyBuffer)
			if err == nil {
				req2.Body = ioutil.NopCloser(&bodyBuffer)
			}
		}
		if checkEqual {
			checkRequestsAreEqual(t, req, req2)
		}
	}
	return
}

func checkRequestsAreEqual(t *testing.T, req, req2 *http.Request) {
	assert.Equal(t, req.Method, req2.Method)
	assert.Equal(t, req.Host, req2.Host)
	assert.Equal(t, req.ContentLength, req2.ContentLength)
	assert.Equal(t, req.Header, req2.Header)
	if body1, err1 := ioutil.ReadAll(req.Body); err1 == nil {
		if body2, err2 := ioutil.ReadAll(req2.Body); err2 == nil {
			assert.Equal(t, body1, body2)
		} else {
			assert.NoError(t, err2)
		}
	} else {
		assert.NoError(t, err1)
	}
	assert.Equal(t, req.URL.Host, req2.URL.Host)
	assert.Equal(t, req.URL.Path, req2.URL.Path)
	assert.Equal(t, req.URL.Query(), req2.URL.Query())
}

func Test_FrameData_WriteRequest(t *testing.T) {
	// If Host header is provided but req.Host is not set
	// the header is used to set it, same with ContentLength
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = ""
	req.ContentLength = -1
	req.Header.Add("Content-Length", "123")
	req.Header.Add("Host", "Somehost")
	req2, err := pipeRequest(t, req, false)
	assert.Equal(t, "Somehost", req2.Host)
	assert.Equal(t, int64(123), req2.ContentLength)
	req.Host = req2.Host
	req.ContentLength = req2.ContentLength
	checkRequestsAreEqual(t, req, req2)

	req = httptest.NewRequest("GET", "/foo/?bar=quux", bytes.NewBuffer([]byte("")))
	req.Host = ""
	req.Header.Add("Foo", "Bar")
	req.Header.Add("Content-Length", "0")
	pipeRequest(t, req, true)

	req = httptest.NewRequest("PUT", "/foo/?bar=quux&bar=foo", ioutil.NopCloser(bytes.NewBufferString("Hello world body!")))
	req.ContentLength = -1
	req.AddCookie(&http.Cookie{Name: "FooCookie", Value: "FooCookieValue"})
	req.AddCookie(&http.Cookie{Name: "BarCookie", Value: "BarCookieValue"})
	req2, err = pipeRequest(t, req, true)
	cookie, _ := req2.Cookie("FooCookie")
	assert.Equal(t, "FooCookieValue", cookie.Value)
	cookie, _ = req2.Cookie("BarCookie")
	assert.Equal(t, "BarCookieValue", cookie.Value)

	req = httptest.NewRequest("NotReallyAMethod", "/overflow", nil)
	req.ContentLength = -1
	for i := 0; i < 8000; i++ {
		req.Header.Add(fmt.Sprint("Header", i), fmt.Sprint("Value", i))
	}
	_, err = pipeRequest(t, req, true)
	assert.Equal(t, ErrFrameTooBig{}, errors.Cause(err))
}

func Test_FrameData_WriteResponse(t *testing.T) {
	fd := NewFrameData()
	fd.WriteHeader(MaxExchangeID)
	fd.WriteResponse(200, 0, nil)

	h := http.Header{}

	fd.ClearID(MaxExchangeID)
	h.Add("Status", "Meh")
	h.Add("Foo", "bar")
	h.Add("Foo", "quux")
	err := fd.WriteResponse(300, 0, h)
	assert.NoError(t, err)

	fd.Clear()
	fd.WriteHeader(MaxExchangeID)
	h = http.Header{}
	const valueTemplate = "foobarquux-%d"
	for i := 0; i < FrameMaxSize/len(valueTemplate); i++ {
		h.Add("Foo", fmt.Sprintf(valueTemplate, i))
	}
	err = fd.WriteResponse(300, 0, h)
	assert.Error(t, err)
}

func Test_FrameData_SetSizeValue(t *testing.T) {
	fd := NewFrameData()
	fd.WriteMuxerControl(MuxerControlPing)
	n := time.Now().UnixNano()
	fd.WriteInt64(n)
	fd.SetSizeValue()
	assert.True(t, fd.Header().IsMuxerControl())
	assert.Equal(t, MuxerControlPing, fd.Header().MuxerControl())
	assert.True(t, fd.Header().HasPayload())
	assert.NotZero(t, fd.Header().SizeValue())
}

func Test_FrameData_SetSizeValue_too_small_fd(t *testing.T) {
	fd := NewFrameData()
	assert.Panics(t, func() { fd.SetSizeValue() })
}
