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
	fd1 := FrameDataAllocID(MaxConnID)
	assert.Equal(t, ConnID(MaxConnID), fd1.Header().ConnID())
	fd2 := FrameDataAllocID(MaxConnID - 1)
	assert.Equal(t, ConnID(MaxConnID-1), fd2.Header().ConnID())
	FrameDataFree(fd1)
	FrameDataFree(fd2)
}

func Test_FramePool_FrameDataFree_Overflow(t *testing.T) {
	// make sure the frameDataPool is full
	for len(frameDataPool) < cap(frameDataPool) {
		FrameDataFree(NewFrameData())
	}
	assert.Equal(t, cap(frameDataPool), len(frameDataPool))
	fd1 := FrameDataAlloc()
	assert.NotNil(t, fd1)
	assert.Equal(t, cap(frameDataPool)-1, len(frameDataPool))
	FrameDataFree(fd1)
	assert.Equal(t, cap(frameDataPool), len(frameDataPool))
	fd2 := NewFrameData()
	assert.NotNil(t, fd2)
	assert.Equal(t, cap(frameDataPool), len(frameDataPool))
	FrameDataFree(fd2)
	assert.Equal(t, cap(frameDataPool), len(frameDataPool))
}
