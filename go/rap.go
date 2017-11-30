// Package rap implements the REST Aggregation Protocol.
package rap

import "time"

const (
	// ConnExchangeID is the Exchange ID used to mark Conn control frames.
	ConnExchangeID = ExchangeID(0xff&(^FrameFlagMask))<<8 | ExchangeID(0xff)
	// ProtocolMaxExchangeID is the maximum value allowed for MaxExchangeID.
	ProtocolMaxExchangeID = ConnExchangeID - 1
	// ProtocolMaxConcurrentExchanges is a theoretical maximum concurrent exchanges.
	ProtocolMaxConcurrentExchanges = 10 * 1000000
	// MaxSendWindowSize is the maximum value allowed for SendWindowSize.
	MaxSendWindowSize = 8
	// FrameHeaderSize is the number of bytes in a frame header.
	FrameHeaderSize = 4
	// FrameMaxSize is the largest buffer size allowed for a full frame.
	FrameMaxSize = 0x10000 // usually 0x10000
	// FrameMaxPayloadSize is the maximum number of bytes in a frame payload.
	FrameMaxPayloadSize = FrameMaxSize - FrameHeaderSize
	// ReadTimeout is how long to wait for a response
	ReadTimeout = time.Second * 5
	// WriteTimeout is how long to wait to send
	WriteTimeout = time.Second * 5
)

var (
	// MaxExchangeID is the highest allowable ExchangeID (configurable).
	MaxExchangeID = ExchangeID(ProtocolMaxExchangeID) // usually ProtocolMaxExchangeID
	// SendWindowSize is the maximum number of frames allowed in flight.
	SendWindowSize = MaxSendWindowSize // usually MaxSendWindowSize
)

