// frameheader.go

// A frame header consists of four bytes. First byte is the flow-control bit
// and high seven bits of the size data. Second byte is low eight bits of the
// size data. Third byte is high eight bits of Conn ID, and fourth byte
// is the low eight bits of the Conn ID.
//
// If the flow-control bit is set, the frame has no actual payload, but is
// simply acknowledging receipt of the given number of bytes.
//
// Flow control is implemented by allowing up to FrameMaxSize bytes in
// transit per Conn. If the limit would be exceeded, block on sending.
// Before sending, increment the counter with the frame size. On receipt of a
// control flow frame, decrement the counter with the indicated number of
// bytes.

package rap

import "fmt"

/*

FrameHeader is 32 bits, divided into a 16-bit Size value, a 3-bit
control field and a 13-bit Conn ID. If the ID is 0x1fff (highest
possible), the frame is a *Muxer* control frame and the control field
is a 3-bit MSB value specifying the frame type:

* 000 - Panic, sender is shutting down due to error, Size is bytes of optional technical information
* 001 - reserved, but expect Size to reflect payload size
* 010 - Ping, Size is bytes of payload data to return in a Pong
* 011 - Pong, Size is bytes of payload data as received in the Ping
* 100 - reserved, ignore the Size value
* 101 - reserved, ignore the Size value
* 110 - reserved, ignore the Size value
* 111 - reserved, ignore the Size value

If Index is 0..0x1ffe (inclusive), the frame applies to that Conn, and
the control field is mapped to three flags: Flow, Body and Head. The following
table lists the valid flag combinations and their meaning:

* 000 - () *reserved*, expect Size to reflect payload size
* 001 - (Head) the data bytes starts with a RAP *record*, without any body bytes
* 010 - (Body) data bytes form body data, no RAP *record* present
* 011 - (Head|Body) data bytes starts with a RAP *record*, remaining bytes form body data
* 100 - (Flow) flow control acknowledging the receipt of a data frame
* 101 - (Flow|Head) *reserved*, ignore the Size value
* 110 - (Flow|Body) final frame, requesting a ack in the form of a (Flow|Body)
* 111 - (Flow|Body|Head) final frame, sent in response to a (Flow|Head), no response may be sent

*/
type FrameHeader []byte

// FrameFlag enumerates the flags used in the frame control bits.
type FrameFlag byte

const (
	// FrameFlagHead indicates the presence of a frame head record at the start
	// of the frame payload when the FLow flag is not set.
	FrameFlagHead FrameFlag = 0x20
	// FrameFlagBody indicates the presence of body data in the frame payload
	// when the FLow flag is not set If the frame payload also has a Head record,
	// the body data starts after it.
	FrameFlagBody FrameFlag = 0x40
	// FrameFlagFlow signals the final frame for a Conn.
	FrameFlagFlow FrameFlag = 0x80
	// FrameFlagMask is a byte mask of the bits used in the third header byte.
	FrameFlagMask = byte(FrameFlagFlow | FrameFlagBody | FrameFlagHead)
)

// MuxerControl enumerates the different types of Muxer control frames.
type MuxerControl byte

const (
	// MuxerControlPanic means sender is shutting down due to error,
	// Size is bytes of optional technical information. Abort all active requests
	// and log the technical information, if available.
	MuxerControlPanic MuxerControl = MuxerControl(0)
	// Unused but reserved for future use, Size contains payload size.
	muxerControlReserved001 MuxerControl = MuxerControl(FrameFlagHead)
	// MuxerControlPing requests a Pong in response with the same payload
	// as this Ping message. Note that the other side may choose to
	// not respond to all Pings.
	MuxerControlPing MuxerControl = MuxerControl(FrameFlagBody)
	// MuxerControlPong is in response to a Ping. The Size value must be the
	// same as the Size value for last received Ping.
	MuxerControlPong MuxerControl = MuxerControl(FrameFlagBody | FrameFlagHead)
	// Unused but reserved for future use, ignore Size value
	muxerControlReserved100 MuxerControl = MuxerControl(FrameFlagFlow)
	// Unused but reserved for future use, ignore Size value
	muxerControlReserved101 MuxerControl = MuxerControl(FrameFlagFlow | FrameFlagHead)
	// Unused but reserved for future use, ignore Size value
	muxerControlReserved110 MuxerControl = MuxerControl(FrameFlagFlow | FrameFlagBody)
	// Unused but reserved for future use, ignore Size value
	muxerControlReserved111 MuxerControl = MuxerControl(FrameFlagFlow | FrameFlagBody | FrameFlagHead)
)

var muxerControlTexts = map[MuxerControl]string{
	MuxerControlPanic:       "Panic",
	muxerControlReserved001: "Rsvd001",
	MuxerControlPing:        "Ping",
	MuxerControlPong:        "Pong",
	muxerControlReserved100: "Rsvd100",
	muxerControlReserved101: "Rsvd101",
	muxerControlReserved110: "Rsvd110",
	muxerControlReserved111: "Rsvd111",
}

var muxerFlagTexts = map[FrameFlag]string{
	(0):                             "...",
	(FrameFlagHead):                 "..H",
	(FrameFlagBody):                 ".B.",
	(FrameFlagBody | FrameFlagHead): ".BH",
	(FrameFlagFlow):                 "F..",
	(FrameFlagFlow | FrameFlagHead): "F.H",
	(FrameFlagFlow | FrameFlagBody): "FB.",
	(FrameFlagFlow | FrameFlagBody | FrameFlagHead): "FBH",
}

func (fh FrameHeader) String() string {
	var midText string
	if fh.IsMuxerControl() {
		midText = muxerControlTexts[fh.MuxerControl()]
	} else {
		midText = muxerFlagTexts[fh.FrameControl()]
	}
	return fmt.Sprintf("[FrameHeader %s %s %d (%d)]", fh.ConnID(), midText, fh.SizeValue(), len(fh))
}

// returns the 16-bit value stored in bytes 0 and 1
func (fh FrameHeader) getLargeValue() uint16 {
	return uint16(fh[0])<<8 | uint16(fh[1])
}

// sets the 16-bit value stored in bytes 0 and 1
func (fh FrameHeader) setLargeValue(n uint16) {
	fh[0] = byte(n >> 8)
	fh[1] = byte(n)
}

// gets the 13-bit value stored in LSB of bytes 2 and 3
func (fh FrameHeader) getSmallValue() uint16 {
	return uint16(fh[2]&(^FrameFlagMask))<<8 | uint16(fh[3])
}

// sets the 13-bit value stored in LSB of bytes 2 and 3
func (fh FrameHeader) setSmallValue(n uint16) {
	fh[2] = (fh[2] & FrameFlagMask) | byte(n>>8)
	fh[3] = byte(n)
}

// SizeValue returns the Size value of the frame.
// This is valid for both MuxerControl frames and data frames.
func (fh FrameHeader) SizeValue() int {
	return int(fh.getLargeValue())
}

// SetSizeValue sets the Size value of the header.
// This is valid for both MuxerControl frames and data frames.
func (fh FrameHeader) SetSizeValue(n int) {
	fh.setLargeValue(uint16(n))
}

// ConnID returns the Conn ID of the frame.
// This is valid for both MuxerControl frames and data frames.
func (fh FrameHeader) ConnID() ConnID {
	return ConnID(fh.getSmallValue())
}

// SetConnID sets the Conn ID.
// This is valid for both MuxerControl frames and data frames.
func (fh FrameHeader) SetConnID(connID ConnID) {
	if connID > MuxerConnID {
		panic("SetConnID(): connID > MuxerConnID")
	}
	fh.setSmallValue(uint16(connID))
}

// HasPayload returns true if the Size value is used for payload size,
// and either the Body or Head bit is set.
// This is valid for both MuxerControl frames and data frames.
func (fh FrameHeader) HasPayload() bool {
	return !fh.HasFlow() && fh.HasBodyOrHead()
}

// IsAck returns true if the frame is acknowledging a sent frame.
func (fh FrameHeader) IsAck() bool {
	return FrameFlag(fh[2]&FrameFlagMask) == FrameFlagFlow
}

// IsFinal returns true if the frame is a final frame (either type).
func (fh FrameHeader) IsFinal() bool {
	return FrameFlag(fh[2]&byte(FrameFlagFlow|FrameFlagBody)) == FrameFlagFlow|FrameFlagBody
}

// IsFinalAck returns true if the frame is a final frame acknowledgement,
// and no response is to be sent.
func (fh FrameHeader) IsFinalAck() bool {
	return fh[2]&byte(FrameFlagMask) == FrameFlagMask
}

// PayloadSize returns the number of payload bytes for a frame.
// If you have ensured HasPayload() returns true, use SizeValue() directly.
// This is valid for both MuxerControl frames and data frames.
func (fh FrameHeader) PayloadSize() (n int) {
	if fh.HasPayload() {
		n = int(fh.SizeValue())
	}
	return
}

// IsMuxerControl returns true if the Conn ID indicates this is a Muxer control frame.
func (fh FrameHeader) IsMuxerControl() bool {
	return fh.ConnID() == MuxerConnID
}

// MuxerControl returns the frame control bits as a MuxerControl value.
// Only valid for Muxer control frames where IsMuxerControl() returns true.
func (fh FrameHeader) MuxerControl() MuxerControl {
	return MuxerControl(fh[2] & FrameFlagMask)
}

// FrameControl returns the frame control bits as a FrameControl bitmask.
// Only valid for Muxer control frames where IsMuxerControl() returns false.
func (fh FrameHeader) FrameControl() FrameFlag {
	return FrameFlag(fh[2] & FrameFlagMask)
}

// SetMuxerControl sets the frame header to a Muxer control frame.
// This sets the control bits and also sets the Conn ID to MuxerConnID.
func (fh FrameHeader) SetMuxerControl(sc MuxerControl) {
	fh[2] = (fh[2] & (^FrameFlagMask)) | byte(sc)
	fh.SetConnID(MuxerConnID)
}

// HasFlow returns true if the Flow bit is set in the frame header.
// Only valid for data frames where IsMuxerControl() returns false.
func (fh FrameHeader) HasFlow() bool {
	return FrameFlag(fh[2])&FrameFlagFlow == FrameFlagFlow
}

// SetFlow sets the Flow bit in the header.
// Only valid for data frames where IsMuxerControl() returns false.
func (fh FrameHeader) SetFlow() {
	fh[2] |= byte(FrameFlagFlow)
}

// HasBodyOrHead returns true if either the Body or Head bits are set.
func (fh FrameHeader) HasBodyOrHead() bool {
	return FrameFlag(fh[2])&(FrameFlagBody|FrameFlagHead) != 0
}

// HasHead returns true if the Head bit is set in the frame header.
// Only valid for data frames where IsMuxerControl() returns false.
func (fh FrameHeader) HasHead() bool {
	return FrameFlag(fh[2])&FrameFlagHead == FrameFlagHead
}

// SetHead sets the Head bit in the header. Only valid for data frames.
func (fh FrameHeader) SetHead() {
	fh[2] |= byte(FrameFlagHead)
}

// HasBody returns true if the Body bit is set in the frame header.
// Only valid for data frames where IsMuxerControl() returns false.
func (fh FrameHeader) HasBody() bool {
	return FrameFlag(fh[2])&FrameFlagBody == FrameFlagBody
}

// SetBody sets the Body bit in the header. Only valid for data frames.
func (fh FrameHeader) SetBody() {
	fh[2] |= byte(FrameFlagBody)
}

// Clear zeroes out the frameheader bytes.
func (fh FrameHeader) Clear() {
	fh[0] = byte(0)
	fh[1] = byte(0)
	fh[2] = byte(0)
	fh[3] = byte(0)
}

// ClearID zeroes out the frameheader bytes and sets the ConnID.
func (fh FrameHeader) ClearID(connID ConnID) {
	if connID > MaxConnID {
		panic("AppendFrameHeader(): connID > MaxConnID")
	}
	fh.Clear()
	fh.SetConnID(connID)
}
