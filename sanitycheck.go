//+build race

package rap

// sanity check the configuration
func init() {
	if ProtocolMaxConcurrentExchanges < 1 {
		panic("ProtocolMaxConcurrentExchanges < 1")
	}
	if SendWindowSize < 1 {
		panic("SendWindowSize < 1")
	}
	if SendWindowSize > MaxSendWindowSize {
		panic("SendWindowSize > MaxSendWindowSize")
	}
	if MuxerExchangeID < 1 {
		panic("MuxerExchangeID < 1")
	}
	if ProtocolMaxExchangeID < 1 {
		panic("ProtocolMaxExchangeID < 1")
	}
	if ProtocolMaxExchangeID >= MuxerExchangeID {
		panic("ProtocolMaxExchangeID >= MuxerExchangeID")
	}
	if MaxExchangeID < 0 {
		panic("MaxExchangeID < 0")
	}
	if MaxExchangeID > ProtocolMaxExchangeID {
		panic("MaxExchangeID > ProtocolMaxExchangeID")
	}
	if FrameMaxSize < FrameHeaderSize+60 {
		panic("FrameMaxSize < FrameHeaderSize+60")
	}
	if FrameMaxSize > FrameHeaderSize+0xffff {
		panic("FrameMaxSize > FrameHeaderSize+0xffff")
	}
}
