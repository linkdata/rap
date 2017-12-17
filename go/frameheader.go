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
	// Unused but reserved for future use, no payload.
	connControlReserved000 ConnControl = ConnControl(0)
	// Unused but reserved for future use, Size contains payload size.
	connControlReserved001 ConnControl = ConnControl(FrameFlagBody)
	// ConnControlPing requests a Pong in response with the same payload
	// as this Ping message. Note that the other side may choose to
	// not respond to all Pings.
	ConnControlPing ConnControl = ConnControl(FrameFlagHead)
	// ConnControlPong is in response to a Ping. The Size value must be the
	// same as the Size value for last received Ping.
	ConnControlPong ConnControl = ConnControl(FrameFlagHead | FrameFlagBody)
	// Unused but reserved for future use, no payload.
	connControlReserved100 ConnControl = ConnControl(FrameFlagFinal)
	// Unused but reserved for future use, Size contains payload size.
	connControlReserved101 ConnControl = ConnControl(FrameFlagFinal | FrameFlagBody)
	// ConnControlStopping means Conn is closing. receiver may not start new requests,
	// Size is bytes of optional html message to display.
	connControlReserved110 ConnControl = ConnControl(FrameFlagFinal | FrameFlagHead)
	// ConnControlPanic means sender is shutting down due to error,
	// Size is bytes of optional technical information. Abort all active requests
	// and log the technical information, if available.
	ConnControlPanic ConnControl = ConnControl(FrameFlagFinal | FrameFlagHead | FrameFlagBody)
)

var connControlTexts = map[ConnControl]string{
	connControlReserved000: "Rsvd000",
	connControlReserved001: "Rsvd001",
	ConnControlPing:        "Ping",
	ConnControlPong:        "Pong",
	connControlReserved100: "Rsvd100",
	connControlReserved101: "Rsvd101",
	connControlReserved110: "Rsvd110",
	ConnControlPanic:       "Panic",
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
func (fh FrameHeader) SizeValue() int {
	return int(fh[0])<<8 | int(fh[1])
}

// SetSizeValue sets the Size value of the header.
// This is valid for both ConnControl frames and data frames.
func (fh FrameHeader) SetSizeValue(n int) {
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

/*
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
*/

// Clear zeroes out the frameheader bytes.
func (fh FrameHeader) Clear() {
	fh[0] = byte(0)
	fh[1] = byte(0)
	fh[2] = byte(0)
	fh[3] = byte(0)
}

// ClearID zeroes out the frameheader bytes and sets the ExchangeID.
func (fh FrameHeader) ClearID(exchangeID ExchangeID) {
	if exchangeID > MaxExchangeID {
		panic("AppendFrameHeader(): exchangeID > MaxExchangeID")
	}
	fh[0] = byte(0)
	fh[1] = byte(0)
	fh[2] = byte(exchangeID >> 8)
	fh[3] = byte(exchangeID)
}
