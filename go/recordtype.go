package rap

// RecordType enumerates the known frame head record types
type RecordType byte

const (
	// RecordTypeInvalid is not usable and if sent will abort the connection
	RecordTypeInvalid = RecordType(0x00)
	// RecordTypeSetString sets an entry in the string lookup table for sending
	RecordTypeSetString = RecordType(0x01)
	// RecordTypeHTTPRequest is a HTTP request record
	RecordTypeHTTPRequest = RecordType(0x02)
	// RecordTypeHTTPResponse is a HTTP response record
	RecordTypeHTTPResponse = RecordType(0x03)
	// RecordTypeUserFirst is the first record type value reserved for user records
	RecordTypeUserFirst = RecordType(0x80)
)
