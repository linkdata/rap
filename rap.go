// Package rap implements the REST Aggregation Protocol.
package rap

import "time"

const (
	// MuxerConnID is the Conn ID used to mark Muxer control frames.
	MuxerConnID = ConnID((0xff & (^ConnID(FrameFlagMask)) << 8) | 0xff)
	// ProtocolMaxConnID is the maximum value allowed for MaxConnID.
	ProtocolMaxConnID = MuxerConnID - 1
	// ProtocolMaxConcurrentConns is an artifical limit on maximum concurrent Conns.
	ProtocolMaxConcurrentConns = 10 * 1000000
	// MaxSendWindowSize is the maximum value allowed for SendWindowSize.
	MaxSendWindowSize = 8
	// FrameHeaderSize is the number of bytes in a frame header.
	FrameHeaderSize = 4
	// FrameMaxSize is the largest buffer size allowed for a full frame.
	FrameMaxSize = 0x10000 // (int(0xff&(^FrameFlagMask))<<8 | int(0xff)) - 16 // usually 0x10000
	// FrameMaxPayloadSize is the maximum number of bytes in a frame payload.
	FrameMaxPayloadSize = FrameMaxSize - FrameHeaderSize
	// DefaultReadTimeout is how long to wait for a response
	DefaultReadTimeout = time.Second * 5
	// DefaultWriteTimeout is how long to wait to send
	DefaultWriteTimeout = time.Second * 5
)

var (
	// MaxConnID is the highest allowable ConnID (configurable).
	MaxConnID = ConnID(ProtocolMaxConnID) // usually ProtocolMaxConnID
	// SendWindowSize is the maximum number of frames allowed in flight.
	SendWindowSize = MaxSendWindowSize // usually MaxSendWindowSize
)
