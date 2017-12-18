package rap

import "testing"
import "github.com/stretchr/testify/assert"

func Test_ResponseWriter_NewResponseWriter(t *testing.T) {
	rw := NewResponseWriter(nil)
	assert.NotNil(t, rw)
}

func Test_ResponseWriter_Header(t *testing.T) {
	rw := &ResponseWriter{}
	assert.NotNil(t, rw.Header())
}

func Test_ResponseWriter_Write_WriteHeader(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	rw := NewResponseWriter(et.Exchange)
	rw.HeaderMap.Add("Content-Length", "Foo")
	n, err := rw.Write([]byte{0x00})
	assert.Equal(t, 1, n)
	assert.NoError(t, err)
}

func Test_ResponseWriter_Flush_Reset(t *testing.T) {
	et := newExchangeTester(t)
	defer et.Close()
	rw := NewResponseWriter(et.Exchange)
	rw.Flush()
	rw.Reset()
}
