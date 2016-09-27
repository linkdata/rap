package rap

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
)

// ReadRequest reads a request structure from a FrameReader and returns a http.Request.
func (fr *FrameReader) ReadRequest() (req *http.Request, err error) {
	methodString, _ := fr.ReadString()
	urlString, _ := fr.ReadString()

	u, err := url.Parse(urlString)
	if err != nil {
		log.Fatal("FrameReader.ReadRequest(): ", err.Error())
		return
	}
	u.Scheme = "http"

	req = &http.Request{
		Method:     methodString,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Close:      false,
	}

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
			u.Query().Add(queryKey, queryValue)
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
		u.Host = host
		req.Header["Host"] = []string{host}
	}
	req.ContentLength = fr.ReadInt64()
	if req.ContentLength == 0 {
		req.Header["Content-Length"] = zeroHeaderValue
	} else if req.ContentLength > 0 {
		req.Header["Content-Length"] = []string{strconv.FormatInt(req.ContentLength, 10)}
	}
	return
}


// ProxyResponse reads a RAP response record and writes it to a http.ResponseWriter
func (fr *FrameReader) ProxyResponse(w http.ResponseWriter) {
	header := w.Header()
	statusCode := int(fr.ReadUint16())
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
			header.Add(headerKey, headerValue)
		}
	}

	if statusText, isNull := fr.ReadString(); !isNull && !hasStatus {
		header["Status"] = []string{statusText}
	}

	if contentLength := fr.ReadInt64(); contentLength >= 0 && !hasCL {
		if contentLength == 0 {
			header["Content-Length"] = zeroHeaderValue
		} else {
			header["Content-Length"] = []string{strconv.FormatInt(contentLength, 10)}
		}
	}

	w.WriteHeader(statusCode)
	return
}
