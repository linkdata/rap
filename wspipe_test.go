package rap

import (
	"log"
	"net/http"
	"net/url"
	"testing"

	"github.com/koding/websocketproxy"
)

type wsPipe struct {
	srvAddr string
	s       *Server
	c       *Client
}

func Test_WsPipe(t *testing.T) {
	// external client -> http.Server -> rap.Client -> rap.Server -> httputil.ReverseProxy -> external server

	pprofsync.Do(func() {
		go func() {
			log.Println(http.ListenAndServe("localhost:6060", nil))
		}()
	})

	rpURL, err := url.Parse("ws://127.0.0.1:9001/")
	if err != nil {
		panic(err)
	}

	proxy := websocketproxy.NewProxy(rpURL)

	p := &wsPipe{
		srvAddr: "127.0.0.1:10111",
		s: &Server{
			Addr:    srvAddr,
			Handler: proxy,
		},
	}

	defer p.s.Close()
	go func() {
		if ln, err := p.s.Listen(p.srvAddr); err == nil {
			err = p.s.Serve(ln)
		}
		if err != nil {
			panic(err)
		}
	}()

	p.c = NewClient(p.srvAddr)
	defer p.c.Close()

	hs := &http.Server{
		Addr:    "127.0.0.1:9002",
		Handler: p.c,
	}
	defer hs.Close()
	hs.ListenAndServe()
}
