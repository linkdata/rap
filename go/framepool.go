package rap

// NewFrameData allocates a new FrameData.
func newFrameData() FrameData {
	return FrameData(make([]byte, FrameHeaderSize, FrameMaxSize))
}

// Provides a buffer of allocated but unused FrameData.
var frameDataPool chan FrameData

func init() {
	chanBufSize := (FrameDataPoolSizeInMB * 1024 * 1024) / FrameMaxSize
	if chanBufSize > 0x10000 {
		chanBufSize = 0x10000
	}
	frameDataPool = make(chan FrameData, chanBufSize)
}

// FrameDataAlloc allocates a FrameData.
func FrameDataAlloc() FrameData {
	select {
	case fd := <-frameDataPool:
		return fd[:FrameHeaderSize]
	default:
		return newFrameData()
	}
}

// FrameDataFree releases a FrameData.
func FrameDataFree(fd FrameData) {
	select {
	case frameDataPool <- fd:
	default:
	}
}

/*
var frameDataPool = sync.Pool{
	New: func() interface{} {
		return newFrameData()
	},
}

// FrameDataAlloc allocates a FrameData.
func FrameDataAlloc() FrameData {
	return (frameDataPool.Get()).(FrameData)
}

// FrameDataFree releases a FrameData.
func FrameDataFree(fd FrameData) {
	frameDataPool.Put(fd)
}
*/
