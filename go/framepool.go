package rap

// Provides a buffer of allocated but unused FrameData.
var frameDataPool chan FrameData

func init() {
	chanBufSize := (FrameDataPoolSizeInMB * 1024 * 1024) / FrameMaxSize
	if chanBufSize > 0x10000 {
		chanBufSize = 0x10000
	}
	frameDataPool = make(chan FrameData, chanBufSize)
}

// FrameDataAlloc allocates an empty FrameData, without a FrameHeader.
func FrameDataAlloc() FrameData {
	select {
	case fd := <-frameDataPool:
		fd.Clear()
		return fd
	default:
		return NewFrameData()
	}
}

// FrameDataAllocID allocates a FrameData with a FrameHeader and the given ExchangeID set.
func FrameDataAllocID(id ExchangeID) FrameData {
	select {
	case fd := <-frameDataPool:
		fd.ClearID(id)
		return fd
	default:
		return NewFrameDataID(id)
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
