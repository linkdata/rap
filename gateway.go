package rap

import (
	"log"
	"net/http"
	"strings"
	"sync"
)

// Gateway maintains one or more Streams to the upstream server.
// A Gateway receives incoming HTTP requests and multiplexes these
// onto one or more Streams. It may create new Streams as needed.
type Gateway struct {
	Client *Client
}

// NewGateway returns a new RAP Gateway. The Gateway implements the
// http.Handler interface, and will forward HTTP requests to the
// upstream RAP server at the address given, relaying the responses
// back to the HTTP clients.
func NewGateway(addr string) *Gateway {
	return &Gateway{
		Client: NewClient(addr),
	}
}

// Close closes the gateway's connections.
func (g *Gateway) Close() error {
	return g.Client.Close()
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// log.Print("Gateway.ServeHTTP() ", r)
	e := g.Client.NewExchange()

	if e == nil {
		var err error
		e, err = g.Client.NewExchangeMayDial()
		if e == nil {
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
	}

	defer e.Release() // will do Stop() before release

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

	var requestErr error
	var responseErr error

	if r.ContentLength == 0 {
		// we can run this without a separate goroutine as request has no body.
		requestErr = e.WriteRequest(r)
		responseErr = e.ProxyResponse(w)
	} else {
		// we allow a response to send before request has finished sending,
		// useful when the upstream does stream processing (such as echoing).
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestErr = e.WriteRequest(r)
		}()
		responseErr = e.ProxyResponse(w)
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
		http.Error(w, errorText, http.StatusInternalServerError)
	}
}
