package rap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_FramePool_FrameDataAlloc(t *testing.T) {
	fd1 := FrameDataAlloc()
	FrameDataFree(fd1)
	fd2 := FrameDataAlloc()
	FrameDataFree(fd2)
}

func Test_FramePool_FrameDataAllocID(t *testing.T) {
	fd1 := FrameDataAllocID(0x123)
	assert.Equal(t, ExchangeID(0x123), fd1.Header().ExchangeID())
	fd2 := FrameDataAllocID(0x012)
	assert.Equal(t, ExchangeID(0x12), fd2.Header().ExchangeID())
	FrameDataFree(fd1)
	FrameDataFree(fd2)
}

func Test_FramePool_FrameDataFree_Overflow(t *testing.T) {
	oldSize := len(frameDataPool)
	frameDataPool = make(chan FrameData, 0x10)
	fd1 := FrameDataAlloc()
	assert.NotNil(t, fd1)
	for i := 0; i <= 0x10; i++ {
		FrameDataFree(NewFrameDataID(ExchangeID(i & 0xFFF)))
	}
	fd2 := FrameDataAlloc()
	assert.NotNil(t, fd2)
	frameDataPool = make(chan FrameData, oldSize)
}
