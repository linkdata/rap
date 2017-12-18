package rap

import "testing"
import "github.com/stretchr/testify/assert"
import "time"

const noSrvAddr string = "192.0.2.1:1"

func Test_Client_NewClient(t *testing.T) {
	c := NewClient(noSrvAddr)
	assert.NotNil(t, c)
}

func Test_Client_no_answer(t *testing.T) {
	c := NewClient(noSrvAddr)
	c.DialTimeout = time.Millisecond * 10
	e, err := c.NewExchangeMayDial()
	assert.Nil(t, e)
	assert.Error(t, err)
}

func Test_Client_server_seems_offline(t *testing.T) {
	c := NewClient(noSrvAddr)
	c.DialTimeout = time.Millisecond * 10
	c.firstAttempt = time.Now().Add(-time.Second)
	e, err := c.NewExchangeMayDial()
	assert.Nil(t, e)
	assert.Error(t, err)
}
