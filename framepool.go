// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

package rap

// Provides a buffer of allocated but unused FrameData.
var frameDataPool chan FrameData

func init() {
	frameDataPool = make(chan FrameData, MaxConnID)
}

// FrameDataAlloc allocates an empty FrameData, without a FrameHeader from the global pool.
func FrameDataAlloc() FrameData {
	select {
	case fd := <-frameDataPool:
		fd.Clear()
		return fd
	default:
		return NewFrameData()
	}
}

// FrameDataAllocID allocates a FrameData with a FrameHeader and the given Conn ID set from the global pool.
func FrameDataAllocID(id ConnID) FrameData {
	select {
	case fd := <-frameDataPool:
		fd.ClearID(id)
		return fd
	default:
		return NewFrameDataID(id)
	}
}

// FrameDataFree releases a FrameData to the global pool.
func FrameDataFree(fd FrameData) {
	if fd != nil {
		select {
		case frameDataPool <- fd:
		default:
		}
	}
}
