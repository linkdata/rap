package rap

import (
	"io"
	"log"
	"net/http"
	"runtime/debug"
)

// WriteRequest writes a http.Request to the exchange, including it's Body and
// a final frame.
func (e *Exchange) WriteRequest(r *http.Request) error {
	// log.Print("Exchange.WriteRequest() ", e)
	e.writeStart()

	if err := e.fdw.WriteRequest(r); err != nil {
		log.Print("Exchange.WriteRequest(): fdw.WriteRequest: ", err.Error(), " e.fdw=", e.fdw)
		return err
	}

	if _, err := e.ReadFrom(r.Body); err != nil {
		log.Print("Exchange.WriteRequest(): ReadFrom(r.Body): ", err.Error())
		return err
	}

	if err := e.CloseWrite(); err != nil {
		log.Print("Exchange.WriteRequest(): CloseWrite: ", err.Error())
		return err
	}

	return nil
}

// ProxyResponse reads a HTTP response and it's body from the Exchange data
// and writes it to the given http.ResponseWriter.
func (e *Exchange) ProxyResponse(w http.ResponseWriter) error {
	// log.Print("Exchange.ProxyResponse() ", e)
	if err := e.loadFrameReader(); err != nil {
		return err
	}

	if !e.fdr.Header().HasHead() {
		return ErrMissingFrameHead
	}

	if e.fr.ReadRecordType() != RecordTypeHTTPResponse {
		return ErrUnhandledRecordType
	}

	e.fr.ProxyResponse(w)

	if _, err := e.WriteTo(w); err != nil {
		return err
	}

	return nil
}

// WriteResponse writes a http.Response to the exchange.
func (e *Exchange) WriteResponse(r *http.Response) (err error) {
	// log.Print("Exchange.WriteResponse() ", e)
	return e.WriteResponseData(r.StatusCode, r.ContentLength, r.Header)
}

// WriteResponseData writes a RAP response header.
func (e *Exchange) WriteResponseData(code int, contentLength int64, header http.Header) (err error) {
	// log.Print("Exchange.WriteResponseData() ", e)
	e.writeStart()
	return e.fdw.WriteResponse(code, contentLength, header)
}

func serveHTTP(h http.Handler, rw *ResponseWriter, req *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Print("Exchange.serveHTTP(): ", r)
			debug.PrintStack()
		}
	}()
	h.ServeHTTP(rw, req)
}

func (e *Exchange) serve(h http.Handler) {
	defer e.Release()
	for {
		if err := e.Start(h); err != nil {
			if err != io.EOF {
				log.Print("Exchange.serve() ", e.ID, ": ", err)
			}
			return
		}
		e.Stop()
	}
}

// Start waits for a start frame and then invokes the given http.Handler.
func (e *Exchange) Start(h http.Handler) error {
	// log.Print("Exchange.Start() ", e)
	if err := e.loadFrameReader(); err != nil {
		return err
	}
	if !e.fdr.Header().HasHead() {
		return ErrMissingFrameHead
	}
	e.hasStarted = true
	e.hasReceived = true
	switch e.fr.ReadRecordType() {
	case RecordTypeHTTPRequest:
		req, err := e.fr.ReadRequest()
		if err != nil {
			return err
		}
		req.Body = e
		h.ServeHTTP(&ResponseWriter{Exchange: e}, req)
	default:
		return ErrUnhandledRecordType
	}
	return nil
}
