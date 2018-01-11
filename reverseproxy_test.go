package rap

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_ReverseProxy_NewReverseProxy(t *testing.T) {
	rp := NewReverseProxy(&url.URL{}, 0)
	assert.NotNil(t, rp)
	assert.NotZero(t, cap(rp.transports))
}
