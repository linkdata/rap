package rap

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// ReverseProxy accepts incoming RAP connections and forwards them to a HTTP server.
type ReverseProxy struct {
	*url.URL                        // Upstream server URL
	transports chan *http.Transport // available http.Transports
}

// NewReverseProxy returns a new ReverseProxy
func NewReverseProxy(u *url.URL, maxConnections int) (rp *ReverseProxy) {
	if maxConnections < 1 {
		maxConnections = 512
	}

	transports := make(chan *http.Transport, maxConnections)
	for i := 0; i < cap(transports); i++ {
		transports <- &http.Transport{
			DisableKeepAlives:   false,
			DisableCompression:  true,
			MaxIdleConnsPerHost: 1,
		}
	}

	return &ReverseProxy{
		URL:        u,
		transports: transports,
	}
}

// ServeHTTP handles a HTTP request and response.
func (rp *ReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Set the request destination to be the upstream.
	req.URL.Scheme = rp.URL.Scheme
	req.URL.Host = rp.URL.Host

	// Remove "Connection: close" and "Keep-Alive:" headers.
	delete(req.Header, "Keep-Alive")
	if len(req.Header["Connection"]) > 0 {
		var newVv []string
		for _, v := range req.Header["Connection"] {
			if strings.ToLower(v) != "close" {
				newVv = append(newVv, v)
			}
		}
		if len(newVv) > 0 {
			req.Header["Connection"] = newVv
		} else {
			delete(req.Header, "Connection")
		}
	}

	// log.Print("ReverseProxy.ServeHTTP(): Request: ", req)
	// TODO: receive incoming frames while waiting
	// TODO: timeout and return 504
	transport := <-rp.transports
	res, err := transport.RoundTrip(req)
	if err != nil {
		log.Print("ReverseProxy.ServeRAP(): RoundTrip(): ", err.Error())
		return
	}
	rp.transports <- transport
	// log.Print("ReverseProxy.ServeHTTP(): Response: ", res)

	dst := rw.Header()
	for k, vv := range res.Header {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	io.Copy(rw, res.Body)
	res.Body.Close()
}
