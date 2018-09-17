package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/linkdata/rap"
)

func serveHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "Not found.", http.StatusNotFound)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, "<html><body>Echo Server</body></html>")
}

func main() {
	rapServer := flag.String("rapserver", "", "the address of the upstream RAP server")
	listenAddr := flag.String("listen", "127.0.0.1:0", "the address the HTTP server should listen on")
	printURL := flag.Bool("printurl", false, "print the listen URL on stdout")

	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveHome)

	c := rap.NewClient(*rapServer)
	defer c.Close()

	ln, err := net.Listen("tcp", *listenAddr)
	defer ln.Close()

	hs := &http.Server{
		Addr:    ln.Addr().String(),
		Handler: c,
	}
	defer hs.Close()

	if *printURL {
		fmt.Fprintln(os.Stdout, "http://", ln.Addr().String(), "/")
	}

	err = hs.Serve(ln)
	if err != nil {
		log.Fatalln(err)
	}
}
