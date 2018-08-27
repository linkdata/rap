package rap

import (
	"log"
	"net/http"
	"testing"
)

type wsPipe struct {
	srvAddr string
	s       *Server
	c       *Client
}

func disabledTestWsPipe(t *testing.T) {
	// external client -> http.Server -> rap.Client -> rap.Server -> gorilla.

	pprofsync.Do(func() {
		go func() {
			log.Println(http.ListenAndServe("localhost:6060", nil))
		}()
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveHome)
	mux.HandleFunc("/c", echoCopyWriterOnly)
	mux.HandleFunc("/f", echoCopyFull)
	mux.HandleFunc("/r", echoReadAllWriter)
	mux.HandleFunc("/m", echoReadAllWriteMessage)
	mux.HandleFunc("/p", echoReadAllWritePreparedMessage)

	p := &wsPipe{
		srvAddr: "127.0.0.1:10111",
		s: &Server{
			Addr:    srvAddr,
			Handler: mux,
		},
	}

	defer p.s.Close()
	go func() {
		if ln, err := p.s.Listen(p.srvAddr); err == nil {
			err = p.s.Serve(ln)
		} else {
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
