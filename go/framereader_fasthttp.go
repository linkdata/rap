package rap

import (
	"github.com/valyala/fasthttp"
)

// ReadFastRequest reads a request structure from a FrameReader and returns a *fasthttp.Request.
func (fr *FrameReader) ReadFastRequest() (req *fasthttp.Request, err error) {
	methodString, _ := fr.ReadString()
	pathString, _ := fr.ReadString()

	req = fasthttp.AcquireRequest()
    req.Header.SetMethod(methodString)
    req.URI().SetPath(pathString)

	for {
		queryKey, isNull := fr.ReadString()
		if isNull {
			break
		}
		// log.Print("FrameReader.ReadRequest() queryKey ", queryKey)
		for {
			queryValue, isNull := fr.ReadString()
			if isNull {
				break
			}
			// log.Print("FrameReader.ReadRequest() queryKey '", queryKey, "' queryValue '", queryValue, "'")
            req.URI().QueryArgs().Add(queryKey, queryValue)
		}
	}

	for {
		headerKey, isNull := fr.ReadString()
		if isNull {
			break
		}
		/*
			// RAP request records headers may not contain Host or Content-Length
			if headerKey == "Host" {
				panic("FrameReader.ReadRequest(): Host in headers")
			}
			if headerKey == "Content-Length" {
				panic("FrameReader.ReadRequest(): Content-Length in headers")
			}
		*/
		// log.Print("FrameReader.ReadRequest() headerKey ", headerKey)
		for {
			headerValue, isNull := fr.ReadString()
			if isNull {
				break
			}
			req.Header.Add(headerKey, headerValue)
		}
	}

	if host, isNull := fr.ReadString(); !isNull {
        req.Header.Add("Host", host)
	}

	req.Header.SetContentLength(int(fr.ReadInt64()))
	return
}


// ProxyFastResponse reads a RAP response record and writes it to a fasthttp.Response
func (fr *FrameReader) ProxyFastResponse(r *fasthttp.Response) {
    r.SetStatusCode(int(fr.ReadUint16()))
	hasCL := false
	hasStatus := false

	for {
		headerKey, isNull := fr.ReadString()
		if isNull {
			break
		}
		if !hasCL && headerKey == "Content-Length" {
			hasCL = true
		}
		if !hasStatus && headerKey == "Status" {
			hasStatus = true
		}
		for {
			headerValue, isNull := fr.ReadString()
			if isNull {
				break
			}
            r.Header.Add(headerKey, headerValue)
		}
	}

	if statusText, isNull := fr.ReadString(); !isNull && !hasStatus {
        r.Header.Add("Status", statusText)
	}

	if contentLength := fr.ReadInt64(); contentLength >= 0 && !hasCL {
        r.Header.SetContentLength(int(contentLength))
	}

	return
}
