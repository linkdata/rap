package rap

import "testing"
import "github.com/stretchr/testify/assert"

func Test_Server_support_functions(t *testing.T) {
	s := &Server{}
	em := s.ServeErrors()
	assert.NotNil(t, em)
	assert.Zero(t, s.ActiveConns())
	assert.Zero(t, s.BytesWritten())
	assert.Zero(t, s.BytesRead())
	s.AddBytesRead(1)
	s.AddBytesWritten(2)
	assert.Equal(t, int64(1), s.BytesRead())
	assert.Equal(t, int64(2), s.BytesWritten())
}
