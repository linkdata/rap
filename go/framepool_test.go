package rap

import (
	"testing"
)

func TestFramePool(t *testing.T) {
	fd1 := FrameDataAlloc()
	FrameDataFree(fd1)
	fd2 := FrameDataAlloc()
	FrameDataFree(fd2)
}
