// Copyright 2018 Johan Lindh. All rights reserved.
// Use of this source code is governed by the MIT license, see the LICENSE file.

//+build race

package rap

// sanity check the configuration
func init() {
	if ProtocolMaxConcurrentConns < 1 {
		panic("ProtocolMaxConcurrentConns < 1")
	}
	if SendWindowSize < 1 {
		panic("SendWindowSize < 1")
	}
	if SendWindowSize > MaxSendWindowSize {
		panic("SendWindowSize > MaxSendWindowSize")
	}
	if MuxerConnID < 1 {
		panic("MuxerConnID < 1")
	}
	if ProtocolMaxConnID < 1 {
		panic("ProtocolMaxConnID < 1")
	}
	if ProtocolMaxConnID >= MuxerConnID {
		panic("ProtocolMaxConnID >= MuxerConnID")
	}
	if MaxConnID < 0 {
		panic("MaxConnID < 0")
	}
	if MaxConnID > ProtocolMaxConnID {
		panic("MaxConnID > ProtocolMaxConnID")
	}
	if FrameMaxSize < FrameHeaderSize+60 {
		panic("FrameMaxSize < FrameHeaderSize+60")
	}
	if FrameMaxSize > FrameHeaderSize+0xffff {
		panic("FrameMaxSize > FrameHeaderSize+0xffff")
	}
}
