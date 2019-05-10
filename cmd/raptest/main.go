package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/linkdata/rap"
)

type echoTester struct {
	Addr   string
	Client *rap.Client
}

func addRequiredHeaders(req *http.Request) {
	if req.Body == nil {
		req.ContentLength = 0
	}
}

func (e echoTester) echoClient(c *rap.Client, req *http.Request) {
	expect := renderRequest(req)
	rr := httptest.NewRecorder()
	c.ServeHTTP(rr, req)
	if body, err := ioutil.ReadAll(rr.Body); err == nil {
		actual := string(body)
		if expect != actual {
			fmt.Printf("expect:\n[%s]\nactual:\n[%s]\n", expect, actual)
		}
	} else {
		fmt.Printf("No echo received, expected:\n[%s]\n", expect)
	}
}

func (e echoTester) echo(req *http.Request) {
	c := e.Client
	if c == nil {
		c = rap.NewClient(e.Addr)
		defer c.Close()
	}
	e.echoClient(c, req)
}

func renderRequest(req *http.Request) string {
	var bodyText string
	if body := req.Body; body != nil {
		contents, _ := ioutil.ReadAll(body)
		bodyText = string(contents)
		req.Body = ioutil.NopCloser(bytes.NewReader(contents))
	}
	var sb strings.Builder
	sb.WriteString(req.Method)
	sb.WriteRune(' ')
	sb.WriteString(req.RequestURI)
	sb.WriteRune('\n')
	foundHost := false
	foundContentLength := false
	for hdr, vals := range req.Header {
		foundHost = foundHost || hdr == "Host"
		foundContentLength = foundContentLength || hdr == "Content-Length"
		sb.WriteString(hdr)
		sb.WriteString(": ")
		for n, val := range vals {
			if n > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(val)
		}
		sb.WriteRune('\n')
	}
	if !foundHost && req.Host != "" {
		sb.WriteString("Host: ")
		sb.WriteString(req.Host)
		sb.WriteRune('\n')
	}
	if !foundContentLength && req.ContentLength >= 0 {
		sb.WriteString("Content-Length: ")
		sb.WriteString(strconv.FormatInt(req.ContentLength, 10))
		sb.WriteRune('\n')
	}
	if bodyText != "" {
		sb.WriteRune('\n')
		sb.WriteString(bodyText)
	}
	return sb.String()
}

func main() {
	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		log.Fatal("missing required argument: address:port of upstream RAP server")
	}
	addr := args[0]
	client := rap.NewClient(addr)
	defer client.Close()

	/*
		if conn, err := client.NewConnMayDial(); err == nil {
			for i := 1; i <= rap.SendWindowSize*2; i++ {
				fmt.Printf("%d: state %s\n", i, conn.String())
				conn.Write([]byte{1})
			}
			fmt.Printf("ending state %s\n", conn.String())
			conn.Close()
		}
	*/

	et := echoTester{Addr: addr}

	//lotsaFooBar := bytes.Repeat([]byte("foobar! "), 8192)
	//et.echo(httptest.NewRequest("POST", "/lotsafoobar", ioutil.NopCloser(bytes.NewReader(lotsaFooBar))))

	et.echo(httptest.NewRequest("GET", "/", nil))
	et.echo(httptest.NewRequest("PUT", "/meh", bytes.NewReader([]byte("foo\nbar"))))
	et.echo(httptest.NewRequest("PUT", "/meh", ioutil.NopCloser(bytes.NewReader([]byte("baz")))))

	for n := 0; n < 1000; n++ {
		// et.echoClient(client, httptest.NewRequest("GET", "/", nil))
		// et.echoClient(client, httptest.NewRequest("PUT", "/meh", ioutil.NopCloser(bytes.NewReader([]byte("foo")))))
	}

	if err := client.Shutdown(); err != nil {
		fmt.Print(err)
	}
}
