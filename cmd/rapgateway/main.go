package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/linkdata/rap"
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:0", "the address the HTTP server should listen on")
	printURL := flag.Bool("printurl", false, "print the listen URL on stdout")

	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		log.Fatal("missing required argument: address:port of upstream RAP server")
	}

	c := rap.NewClient(args[0])
	defer c.Close()

	ln, err := net.Listen("tcp", *listenAddr)
	defer ln.Close()

	hs := &http.Server{
		Addr:    ln.Addr().String(),
		Handler: c,
	}
	defer hs.Close()

	if *printURL {
		fmt.Fprintf(os.Stdout, "http://%s/\n", ln.Addr().String())
	}

	err = hs.Serve(ln)
	if err != nil {
		log.Fatalln(err)
	}
}
