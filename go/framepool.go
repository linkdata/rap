package rap

// Provides a buffer of allocated but unused FrameData.
var frameDataPool chan FrameData

func init() {
	frameDataPool = make(chan FrameData, 0x10000)
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
	if fd != nil {
		select {
		case frameDataPool <- fd:
		default:
		}
	}
}
