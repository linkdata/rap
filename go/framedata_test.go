package rap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewFrameData(t *testing.T) {
	fd := NewFrameData()
	assert.NotNil(t, fd)
	assert.Equal(t, fd.Buffered(), FrameHeaderSize)
	assert.Equal(t, fd.Available(), FrameMaxSize-FrameHeaderSize)
}

func TestNewFrameDataExchangeIDRange(t *testing.T) {
	fd := NewFrameData()
	assert.Equal(t, fd.Header().ExchangeID(), ExchangeID(0))
	fd.WriteHeader(ExchangeID(1))
	assert.Equal(t, fd.Header().ExchangeID(), ExchangeID(1))
	fd.WriteHeader(MaxExchangeID)
	assert.Equal(t, fd.Header().ExchangeID(), MaxExchangeID)
	assert.Panics(t, func() { fd.WriteHeader(ExchangeID(0xFFFF)) })
}
