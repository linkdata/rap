package rap

import (
    "io"
	"log"
	"github.com/valyala/fasthttp"
)

type netHTTPBody struct {
	b []byte
}

func (r *netHTTPBody) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

// WriteFastRequest writes a fasthttp.Request to the exchange, including it's Body and
// a final frame.
func (e *Exchange) WriteFastRequest(req *fasthttp.Request) error {
	e.writeStart()

	if err := e.fdw.WriteFastRequest(req); err != nil {
		log.Print("Exchange.WriteFastRequest(): fdw.WriteRequest: ", err.Error(), " e.fdw=", e.fdw)
		return err
	}

	if _, err := e.ReadFrom(&netHTTPBody{req.Body()}); err != nil {
		log.Print("Exchange.WriteFastRequest(): ReadFrom(r.Body): ", err.Error())
		return err
	}

	if err := e.CloseWrite(); err != nil {
		log.Print("Exchange.WriteFastRequest(): CloseWrite: ", err.Error())
		return err
	}

	return nil
}

// ProxyFastResponse reads a HTTP response and it's body from the Exchange data
// and writes it to the given fasthttp.RequestCtx.
func (e *Exchange) ProxyFastResponse(ctx *fasthttp.RequestCtx) error {
	// log.Print("Exchange.ProxyFastResponse() ", e)
	if err := e.loadFrameReader(); err != nil {
		return err
	}

	if !e.fdr.Header().HasHead() {
		return ErrMissingFrameHead
	}

	if e.fr.ReadRecordType() != RecordTypeHTTPResponse {
		return ErrUnhandledRecordType
	}

	e.fr.ProxyFastResponse(&ctx.Response)

	if _, err := e.WriteTo(ctx); err != nil {
		return err
	}

	return nil
}
