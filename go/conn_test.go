package rap

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

type rwcPipe struct {
	*io.PipeReader
	*io.PipeWriter
}

func (rwcp *rwcPipe) Close() error {
	if err := rwcp.PipeWriter.Close(); err != nil {
		panic(err)
	}
	return rwcp.PipeReader.Close()
}

func newRwcPipes() (a, b *rwcPipe) {
	ra, wa := io.Pipe()
	rb, wb := io.Pipe()
	a = &rwcPipe{
		PipeReader: rb,
		PipeWriter: wa,
	}
	b = &rwcPipe{
		PipeReader: ra,
		PipeWriter: wb,
	}
	return
}

type connTester struct {
	a, b     *rwcPipe
	conn     *Conn
	server   *Conn
	isClosed bool
}

func (ct *connTester) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

func (ct *connTester) Close() (err error) {
	return ct.a.Close()
}

func newConnTester() (ct *connTester) {
	a, b := newRwcPipes()
	ct = &connTester{
		a:      a,
		b:      b,
		conn:   NewConn(a),
		server: NewConn(b),
	}
	go func(ct *connTester) {
		if err := ct.server.ServeHTTP(ct); err != nil {
			return
		}
	}(ct)
	return
}

func Test_Conn_String(t *testing.T) {
	ct := newConnTester()
	defer ct.Close()
	assert.Equal(t, "[Conn]", ct.conn.String())
}
