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

func Test_Client_connect_and_close(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(srvAddr)
	assert.NotNil(t, c)
	c.DialTimeout = time.Second
	e1, err := c.NewExchangeMayDial()
	assert.NoError(t, err)
	assert.NotNil(t, e1)
	defer e1.Release()
	e2 := c.NewExchange()
	assert.NotNil(t, e2)
	defer e2.Release()
	assert.Equal(t, e1.conn, e2.conn)
}

func Test_Client_exhaust_conn(t *testing.T) {
	st := newSrvTester(t)
	defer st.Close()
	c := NewClient(srvAddr)
	assert.NotNil(t, c)
	c.DialTimeout = time.Second

	grabbed := make(chan *Exchange, MaxExchangeID*2+1)
	e, err := c.NewExchangeMayDial()
	firstConn := c.getConn()
	assert.NoError(t, err)
	assert.NotNil(t, e)
	for e != nil {
		grabbed <- e
		e = c.NewExchange()
	}
	assert.Equal(t, int(MaxExchangeID), cap(c.getConn().exchanges))
	assert.Equal(t, 0, len(c.getConn().exchanges))
	e = c.NewExchange()
	assert.Nil(t, e)

	// This will create a new conn since the old is all in use
	e, err = c.NewExchangeMayDial()
	assert.NoError(t, err)
	assert.NotNil(t, e)
	secondConn := c.getConn()
	e.Release()
	assert.NotEqual(t, len(firstConn.exchanges), len(secondConn.exchanges))
	assert.Equal(t, int(MaxExchangeID), cap(c.conn.exchanges))
	assert.Equal(t, 1, len(c.conn.exchanges))

	// Now free all the Exchanges in the old conn
releaseOldConn:
	for {
		select {
		case e = <-grabbed:
			e.Release()
			assert.NotNil(t, e)
			assert.Equal(t, firstConn, e.conn)
		default:
			break releaseOldConn
		}
	}

	// Grab all available exchanges, should be two conn's worth
	e = c.NewExchange()
	assert.NotNil(t, e)
	for e != nil {
		grabbed <- e
		e = c.NewExchange()
	}

	assert.Equal(t, int(MaxExchangeID)*2, len(grabbed))
	for len(grabbed) > 0 {
		(<-grabbed).Release()
	}
}
