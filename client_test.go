package rap

import "testing"
import "github.com/stretchr/testify/assert"

func Test_Client_NewClient(t *testing.T) {
	c := NewClient("")
	assert.NotNil(t, c)
}
