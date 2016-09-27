package rap

import (
	"log"
	"net/http"
	"sync"
	"github.com/valyala/fasthttp"
)

// HandleFastHTTP implements the handler for valyala/fasthttp.
func (g *Gateway) HandleFastHTTP(ctx *fasthttp.RequestCtx) {
	e := g.Client.NewExchange()

	if e == nil {
		var err error
		e, err = g.Client.NewExchangeMayDial()
		if e == nil {
			if err != nil {
				ctx.Error(err.Error(), http.StatusGatewayTimeout)
			} else {
				log.Print("Gateway.ServeHTTP(): can't allocate exchange and nil error")
				ctx.Error("can't allocate exchange", http.StatusInternalServerError)
			}
			return
		}
	}

	defer e.Release() // will do Stop() before release

/*
	// Detect and handle WebSocket requests.
	if len(r.Header["Upgrade"]) > 0 &&
		len(r.Header["Connection"]) > 0 &&
		r.ProtoAtLeast(1, 1) &&
		r.Method == "GET" &&
		strings.ToLower(r.Header["Upgrade"][0]) == "websocket" &&
		strings.ToLower(r.Header["Connection"][0]) == "upgrade" {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "Gateway.ServeHTTP(): http.Hijacker unsupported", http.StatusInternalServerError)
			return
		}
		rwc, buf, err := hj.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rwc.Close()
		br := buf.Reader
		if br.Buffered() > 0 {
			log.Print("Gateway.ServeHTTP(): websocket client sent data before handshake was complete")
			return
		}
		e.initiateWebsocket(rwc, buf, r)
		return
	}
*/

	var requestErr error
	var responseErr error

	if ctx.Request.Header.ContentLength() == 0 {
		// we can run this without a separate goroutine as request has no body.
		requestErr = e.WriteFastRequest(&ctx.Request)
		responseErr = e.ProxyFastResponse(ctx)
	} else {
		// we allow a response to send before request has finished sending,
		// useful when the upstream does stream processing (such as echoing).
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestErr = e.WriteFastRequest(&ctx.Request)
		}()
		responseErr = e.ProxyFastResponse(ctx)
		wg.Wait()
	}

	if responseErr == nil && requestErr == nil {
		return
	}

	errorText := "Gateway.ServeHTTP():"
	if requestErr != nil {
		errorText += " requestErr=" + requestErr.Error()
	}
	if responseErr != nil {
		errorText += " responseErr=" + responseErr.Error()
	}
	log.Print(errorText)
	if !e.hasReceived {
		ctx.Error(errorText, http.StatusInternalServerError)
	}
}
