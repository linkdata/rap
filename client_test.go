package rap

import "testing"
import "github.com/stretchr/testify/assert"
import "time"

func Test_Client_NewClient(t *testing.T) {
	c := NewClient("")
	assert.NotNil(t, c)
}

func Test_Client_no_answer(t *testing.T) {
	c := NewClient("192.0.2.1:1")
	c.DialTimeout = time.Millisecond * 10
	e, err := c.NewExchangeMayDial()
	assert.Nil(t, e)
	assert.Error(t, err)
}

func Test_Client_server_seems_offline(t *testing.T) {
	c := NewClient("192.0.2.1:1")
	c.DialTimeout = time.Millisecond * 10
	c.firstAttempt = time.Now().Add(-time.Second)
	e, err := c.NewExchangeMayDial()
	assert.Nil(t, e)
	assert.Error(t, err)
}
