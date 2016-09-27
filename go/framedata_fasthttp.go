package rap

import (
	"github.com/valyala/fasthttp"
    "io"
)

// fasthttp doesn't use a kvv and treats "x" as a value without a key
// rather than as a key without a value, so we need to transform this.
type argsMap map[string][][]byte
func (am *argsMap) visitor(k, v []byte) {
    var key string
    var val []byte
    if len(k) == 0 {
        key = string(v)
        val = []byte{}
    } else {
        key = string(k)
        val = v
    }
    (*am)[key] = append((*am)[key], val)
}
func (am *argsMap) write(fd *FrameData) {
	for k, vv := range *am {
		fd.WriteString(k)
		for _, v := range vv {
			fd.WriteByteArrayString(v)
		}
		fd.WriteStringNull()
	}
	fd.WriteStringNull()
}

// WriteFastRequest writes a FrameTypeRequest record to a FrameData given a fasthttp.Request.
func (fd *FrameData) WriteFastRequest(req *fasthttp.Request) error {
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPRequest)
	fd.WriteByteArrayString(req.Header.Method())
	fd.WriteByteArrayString(req.URI().Path()) // fasthttp will normalize the path for us

    qm := make(argsMap)
    req.URI().QueryArgs().VisitAll(qm.visitor)
    qm.write(fd)

    host := []byte{}
    hm := make(argsMap)
    req.Header.VisitAll(hm.visitor)
	if hostValues, ok := hm["Host"]; ok {
        host = hostValues[0]
    }
    delete(hm, "Content-Length")
    delete(hm, "Host")
    hm.write(fd)

	if len(host) == 0 {
		fd.WriteStringNull()
	} else {
		fd.WriteByteArrayString(host)
	}

    fd.WriteInt64(int64(req.Header.ContentLength()))

	if len(*fd) > FrameMaxSize {
		return ErrFrameTooBig
	}
	return nil
}

// WriteFastResponse writes a FrameTypeResponse record to a FrameData given a fasthttp.Response.
func (fd *FrameData) WriteFastResponse(code int, contentLength int64, response *fasthttp.Response) error {
	// log.Print("FrameData.WriteFastResponse(", code, ", ", contentLength, ", ", ctx)
	fd.Header().SetHead()
	fd.WriteRecordType(RecordTypeHTTPResponse)
	fd.WriteUint16(uint16(code))

    statusText := []byte{}
    hm := make(argsMap)
    response.Header.VisitAll(hm.visitor)
    statusTextArray := hm["Status"]
    if statusTextArray != nil {
        statusText = statusTextArray[0]
        delete(hm, "Status")
    }
    hm.write(fd)

	if len(statusText) == 0 {
		fd.WriteStringNull()
	} else {
		fd.WriteByteArrayString(statusText)
	}
	fd.WriteInt64(contentLength)
	if len(*fd) > FrameMaxSize {
		return io.ErrShortBuffer
	}
	return nil
}
