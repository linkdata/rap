// frameheader.go

// A frame header consists of four bytes. First byte is the flow-control bit
// and high seven bits of the size data. Second byte is low eight bits of the
// size data. Third byte is high eight bits of Exchange ID, and fourth byte
// is the low eight bits of the Exchange ID.
//
// If the flow-control bit is set, the frame has no actual payload, but is
// simply acknowledging receipt of the given number of bytes.
//
// Flow control is implemented by allowing up to FrameMaxSize bytes in
// transit per exchange. If the limit would be exceeded, block on sending.
// Before sending, increment the counter with the frame size. On receipt of a
// control flow frame, decrement the counter with the indicated number of
// bytes.

package rap

import "fmt"

// FrameHeader provides the interface for manipulating a byte array as such.
// The 32 bits of the frame header is divided into a 16-bit Size value, a 3-bit
// control field and a 13-bit exchange Index.
// If Index is 0x1fff (highest possible), the frame is a stream control frame
// and the control field is a 3-bit MSB value specifying the frame type.
// * 000 - Ping, Size is a number to return in a Pong
// * 001 - Setup, set up string mapping table, Size is bytes of data
// * 010 - Stopping, no new exchanges, Size is bytes of optional UTF-8 message
// * 011 - Stopped, conn closing now, Size is bytes of optional UTF-8 message
// * 100 - Pong, Size is the value received in the Ping
// * 101 - reserved
// * 110 - reserved
// * 111 - reserved
// If Index is 0..0x1ffe (inclusive), the frame applies to that exchange, and
// the control field is mapped to three flags: Final, Head and Body.
// If neither Head nor Body flags are set, the frame is a flow control
// frame and the Size is the number of received bytes being acknowledged.
// * Final - if set, this is the final frame for the exchange
// * Head - if set, the data bytes starts with a head record
// * Body - if set, data bytes form body data (after Head record if present)
type FrameHeader []byte

// FrameFlag enumerates the flags used in the frame control bits.
type FrameFlag byte

const (
	// FrameFlagBody indicates the presence of body data in the frame payload.
	// If the frame payload also has a Head record, the body data starts after it.
	FrameFlagBody FrameFlag = 0x20
	// FrameFlagHead indicates the presence of a frame head record at the start
	// of the frame payload.
	FrameFlagHead FrameFlag = 0x40
	// FrameFlagFinal signals the final frame for an exchange.
	FrameFlagFinal FrameFlag = 0x80
	// FrameFlagMask is a byte mask of the bits used in the third header byte.
	FrameFlagMask = byte(FrameFlagFinal | FrameFlagHead | FrameFlagBody)
)

// ConnControl enumerates the different types of conn control frames.
type ConnControl byte

const (
	// ConnControlPing requests a Pong in response with the same Size
	// value as this Ping message. Note that the other side may choose to
	// not respond to all Pings.
	ConnControlPing ConnControl = ConnControl(0)
	// ConnControlSetup is only valid as the first frame sent, sets up the
	// string translation table.
	ConnControlSetup ConnControl = ConnControl(FrameFlagBody)
	// ConnControlStopping means Conn is closing. No new exchanges may be
	// initiated on the conn and active exchanges must be closed.
	ConnControlStopping ConnControl = ConnControl(FrameFlagHead)
	// ConnControlStopped can optionally be sent before closing a Conn in
	// order to provide an error message in UTF-8. The message should be logged
	// but is not meant for end users to see.
	ConnControlStopped ConnControl = ConnControl(FrameFlagHead | FrameFlagBody)
	// ConnControlPong is in response to a Ping. The Size value must be the
	// same as the Size value for last received Ping.
	ConnControlPong ConnControl = ConnControl(FrameFlagFinal | 0)
	// The following are unused but reserved for future use. They are all payload
	// carrying, so a nonzero Size value is a payload byte count.
	connControlReserved1 ConnControl = ConnControl(FrameFlagFinal | FrameFlagBody)
	connControlReserved2 ConnControl = ConnControl(FrameFlagFinal | FrameFlagHead)
	connControlReserved3 ConnControl = ConnControl(FrameFlagFinal | FrameFlagHead | FrameFlagBody)
)

var connControlTexts = map[ConnControl]string{
	ConnControlPing:      "Ping",
	ConnControlSetup:     "Setup",
	ConnControlStopping:  "Stopping",
	ConnControlStopped:   "Stopped",
	ConnControlPong:      "Pong",
	connControlReserved1: "Reserved1",
	connControlReserved2: "Reserved2",
	connControlReserved3: "Reserved3",
}

var connFlagTexts = map[FrameFlag]string{
	(0):                                              "...",
	(FrameFlagBody):                                  "..B",
	(FrameFlagHead):                                  ".H.",
	(FrameFlagHead | FrameFlagBody):                  ".HB",
	(FrameFlagFinal):                                 "F..",
	(FrameFlagFinal | FrameFlagBody):                 "F.B",
	(FrameFlagFinal | FrameFlagHead):                 "FH.",
	(FrameFlagFinal | FrameFlagHead | FrameFlagBody): "FHB",
}

func (fh FrameHeader) String() string {
	var midText string
	if fh.IsConnControl() {
		midText = connControlTexts[fh.ConnControl()]
	} else {
		midText = connFlagTexts[fh.FrameControl()]
	}
	return fmt.Sprintf("[FrameHeader %s %s %d (%d)]", fh.ExchangeID(), midText, fh.SizeValue(), len(fh))
}

// SizeValue returns the Size value of the frame.
// This is valid for both ConnControl frames and data frames.
func (fh FrameHeader) SizeValue() int32 {
	return int32(fh[0])<<8 | int32(fh[1])
}

// SetSizeValue sets the Size value of the header.
// This is valid for both ConnControl frames and data frames.
func (fh FrameHeader) SetSizeValue(n int32) {
	fh[0] = byte(n >> 8)
	fh[1] = byte(n)
}

// ExchangeID returns the Exchange ID of the frame.
// This is valid for both ConnControl frames and data frames.
func (fh FrameHeader) ExchangeID() ExchangeID {
	return ExchangeID(fh[2]&(^FrameFlagMask))<<8 | ExchangeID(fh[3])
}

// SetExchangeID sets the exchange ID.
// This is valid for both ConnControl frames and data frames.
func (fh FrameHeader) SetExchangeID(exchangeID ExchangeID) {
	if exchangeID > MaxExchangeID {
		panic("SetExchangeID(): exchangeID > MaxExchangeID")
	}
	fh[2] = (fh[2] & FrameFlagMask) | byte(exchangeID>>8)
	fh[3] = byte(exchangeID)
}

// HasPayload returns true if either the Headers or Body bit is set.
// If false, it means the Size value is not used for payload size.
// This is valid for both ConnControl frames and data frames.
func (fh FrameHeader) HasPayload() bool {
	return (fh[2] & byte(FrameFlagHead|FrameFlagBody)) != 0
}

// PayloadSize returns the number of payload bytes for a frame.
// If you have ensured HasPayload() returns true, use SizeValue() directly.
// This is valid for both ConnControl frames and data frames.
func (fh FrameHeader) PayloadSize() int {
	if fh.HasPayload() {
		return int(fh.SizeValue())
	}
	return 0
}

// IsConnControl returns true if the Exchange ID indicates this is a conn control frame.
func (fh FrameHeader) IsConnControl() bool {
	return (fh[3] == 0xff) && ((fh[2] & (^FrameFlagMask)) == (^FrameFlagMask))
}

// ConnControl returns the frame control bits as a ConnControl value.
// Only valid for conn control frames where IsConnControl() returns true.
func (fh FrameHeader) ConnControl() ConnControl {
	return ConnControl(fh[2] & FrameFlagMask)
}

// FrameControl returns the frame control bits as a FrameControl bitmask.
// Only valid for conn control frames where IsConnControl() returns false.
func (fh FrameHeader) FrameControl() FrameFlag {
	return FrameFlag(fh[2] & FrameFlagMask)
}

// SetConnControl sets the frame header to a conn control frame.
// This sets the control bits and also sets the Exchange ID to 0x1fff.
func (fh FrameHeader) SetConnControl(sc ConnControl) {
	fh[2] = byte(sc) | 0x1f
	fh[3] = 0xff
}

// IsFinal returns true if the Final bit is set in the frame header.
// Only valid for data frames where IsConnControl() returns false.
func (fh FrameHeader) IsFinal() bool {
	return FrameFlag(fh[2])&FrameFlagFinal == FrameFlagFinal
}

// SetFinal sets the Final bit in the header. Only valid for data frames.
func (fh FrameHeader) SetFinal() {
	fh[2] |= byte(FrameFlagFinal)
}

// HasHead returns true if the Head bit is set in the frame header.
// Only valid for data frames where IsConnControl() returns false.
func (fh FrameHeader) HasHead() bool {
	return FrameFlag(fh[2])&FrameFlagHead == FrameFlagHead
}

// SetHead sets the Head bit in the header. Only valid for data frames.
func (fh FrameHeader) SetHead() {
	fh[2] |= byte(FrameFlagHead)
}

// HasBody returns true if the Body bit is set in the frame header.
// Only valid for data frames where IsConnControl() returns false.
func (fh FrameHeader) HasBody() bool {
	return FrameFlag(fh[2])&FrameFlagBody == FrameFlagBody
}

// SetBody sets the Body bit in the header. Only valid for data frames.
func (fh FrameHeader) SetBody() {
	fh[2] |= byte(FrameFlagBody)
}

// AppendFrameHeader adds a placeholder frame header to the given array.
func AppendFrameHeader(dst []byte, exchangeID ExchangeID) []byte {
	if exchangeID > MaxExchangeID {
		panic("AppendFrameHeader(): exchangeID > MaxExchangeID")
	}
	return append(dst,
		byte(0),             // MSB size bits
		byte(0),             // LSB size bits
		byte(exchangeID>>8), // control bits, MSB exchange id
		byte(exchangeID),    // LSB exchange id
	)
}
