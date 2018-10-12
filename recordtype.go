package rap

// RecordType enumerates the known frame head record types
type RecordType byte

const (
	// RecordTypeInvalid is not usable and if sent will abort the muxer
	RecordTypeInvalid = RecordType(0x00)
	// RecordTypeSetString sets an entry in the string lookup table for sending
	RecordTypeSetString = RecordType(0x01)
	// RecordTypeSetRoute sets a naoina/denco URL pattern to match
	RecordTypeSetRoute = RecordType(0x02)
	// RecordTypeHTTPRequest is a HTTP request record
	RecordTypeHTTPRequest = RecordType(0x03)
	// RecordTypeHTTPResponse is a HTTP response record
	RecordTypeHTTPResponse = RecordType(0x04)
	// RecordTypeServicePause orders a RAP client to respond to new requests with a canned response
	RecordTypeServicePause = RecordType(0x05)
	// RecordTypeServiceResume lets a RAP client resume normal operations after a pause
	RecordTypeServiceResume = RecordType(0x06)
	// RecordTypeHijacked is sent when a Conn has been Hijack()'ed
	RecordTypeHijacked = RecordType(0x07)
	// RecordTypeUserFirst is the first record type value reserved for user records
	RecordTypeUserFirst = RecordType(0x80)
)
