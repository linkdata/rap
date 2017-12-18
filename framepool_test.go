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
	fd1 := FrameDataAllocID(MaxExchangeID)
	assert.Equal(t, ExchangeID(MaxExchangeID), fd1.Header().ExchangeID())
	fd2 := FrameDataAllocID(MaxExchangeID - 1)
	assert.Equal(t, ExchangeID(MaxExchangeID-1), fd2.Header().ExchangeID())
	FrameDataFree(fd1)
	FrameDataFree(fd2)
}

func Test_FramePool_FrameDataFree_Overflow(t *testing.T) {
	if RaceEnabled() {
		t.Skip("skipping since -race is enabled")
		return
	}
	oldSize := len(frameDataPool)
	var idLimit = int(MaxExchangeID)
	if idLimit > 0x10 {
		idLimit = 0x10
	}
	frameDataPool = make(chan FrameData, idLimit)
	fd1 := FrameDataAlloc()
	assert.NotNil(t, fd1)
	for i := 0; i <= idLimit; i++ {
		FrameDataFree(NewFrameDataID(ExchangeID(i & 0xFFF)))
	}
	fd2 := FrameDataAlloc()
	assert.NotNil(t, fd2)
	frameDataPool = make(chan FrameData, oldSize)
}
