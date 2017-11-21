/*
	Provides a HTTP(S) caching service and multiplexes
	REST requests into an aggregation protocol.

	Data flow schematic, read from top to bottom. The number
	of Clients can be high (millions), hundreds of Gateways,
	single Dispatcher and then Backends as appropriate.

	----------------------------------------------------------
	Client-1 Client-2 Client-3 Client-4 Client-5 Client-6
	  (( HTTP, HTTP(S), HTTP/2 etc over public networks ))
			Gateway-1     Gateway-2     Gateway-3
       (( Aggregation protocol over private networks ))
				         Dispatcher
	                     (( IPC ))
	    Backend-1 Backend-2 Backend-3 Backend-4 Backend-5
	----------------------------------------------------------
*/

package main

import (
	"bytes"
	_ "crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"github.com/linkdata/rap/go"
	"github.com/pkg/profile"
)

const (
	defaultDispatchPort = ":10111"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// echoCopy echoes messages from the client using io.Copy.
func echoCopy(w http.ResponseWriter, r *http.Request, writerOnly bool) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade:", err)
		log.Print(r)
		return
	}
	defer conn.Close()
	for {
		mt, r, err := conn.NextReader()
		if err != nil {
			if err != io.EOF {
				log.Println("NextReader:", err)
			}
			return
		}
		if mt == websocket.TextMessage {
			r = &validator{r: r}
		}
		w, err := conn.NextWriter(mt)
		if err != nil {
			log.Println("NextWriter:", err)
			return
		}
		/*
			if mt == websocket.TextMessage {
				r = &validator{r: r}
			}
		*/
		if writerOnly {
			_, err = io.Copy(struct{ io.Writer }{w}, r)
		} else {
			_, err = io.Copy(w, r)
		}
		if err != nil {
			if err == errInvalidUTF8 {
				conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, ""),
					time.Time{})
			}
			log.Println("Copy:", err)
			return
		}
		err = w.Close()
		if err != nil {
			log.Println("Close:", err)
			return
		}
	}
}

func echoCopyWriterOnly(w http.ResponseWriter, r *http.Request) {
	echoCopy(w, r, true)
}

func echoCopyFull(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	echoCopy(w, r, false)
}

// echoReadAll echoes messages from the client by reading the entire message
// with ioutil.ReadAll.
func echoReadAll(w http.ResponseWriter, r *http.Request, writeMessage bool) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade:", err)
		log.Print(r)
		return
	}
	defer conn.Close()
	for {
		mt, b, err := conn.ReadMessage()
		if err != nil {
			if err != io.EOF {
				log.Println("NextReader:", err)
			}
			return
		}
		if mt == websocket.TextMessage {
			if !utf8.Valid(b) {
				conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, ""),
					time.Time{})
				log.Println("ReadAll: invalid utf8")
			}
		}
		if writeMessage {
			err = conn.WriteMessage(mt, b)
			if err != nil {
				log.Println("WriteMessage:", err)
			}
		} else {
			w, err := conn.NextWriter(mt)
			if err != nil {
				log.Println("NextWriter:", err)
				return
			}
			if _, err := w.Write(b); err != nil {
				log.Println("Writer:", err)
				return
			}
			if err := w.Close(); err != nil {
				log.Println("Close:", err)
				return
			}
		}
	}
}

func echoReadAllWriter(w http.ResponseWriter, r *http.Request) {
	echoReadAll(w, r, false)
}

func echoReadAllWriteMessage(w http.ResponseWriter, r *http.Request) {
	echoReadAll(w, r, true)
}

var maxNumGoroutine int
var allRequestMutex sync.Mutex
var requestCount int64
var allRequests = make(map[int]string)

var mainReply = []byte("<html><body><h1>It works!</h1>\n<p>This is the default web page for this server.</p>\n<p>The web server software is running but no content has been added, yet.</p>\n</body></html>\n\n")
var mainReplyLength = strconv.Itoa(len(mainReply))
var helloWorld = []byte("Hello World!")

func serveHome(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	atomic.AddInt64(&requestCount, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", mainReplyLength)
	w.Write(mainReply)
}

func servePost(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	atomic.AddInt64(&requestCount, 1)
	n, err := io.Copy(ioutil.Discard, r.Body)
	log.Print("servePost() read ", n, " bytes, err ", err)
	w.WriteHeader(200)
}

func serveGwTest(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	atomic.AddInt64(&requestCount, 1)
	if false {
		time.Sleep(time.Second)
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", "28")
	w.Write([]byte("RAP gateway test response :)"))
}

func serveReturn(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	atomic.AddInt64(&requestCount, 1)
	retcode, _ := strconv.Atoi(ps.ByName("retcode"))
	w.WriteHeader(retcode)
}

func serveMax(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&requestCount, 1)
	if x := runtime.NumGoroutine(); x > maxNumGoroutine {
		maxNumGoroutine = x
	}
	w.Header().Set("Content-Type", "text/plain")
	/*
		buffer := make([]byte, 256)
		p_buffer := (*C.char)(unsafe.Pointer(&buffer[0]))
		C.scv8_app_init(p_buffer, C.size_t(len(buffer)))
		n := bytes.Index(buffer, []byte{0})
	*/
	fmt.Fprintln(w, "<html><body>\n", "maxNumGoroutine", maxNumGoroutine, "\n", "</body></html>")
	return
}

func modeGateway(addr string) {
	if !strings.Contains(addr, ":") {
		addr += defaultDispatchPort
	}

	gw := rap.NewGateway(addr)

	go func() {
		s := &http.Server{
			Addr:    *flagHTTP,
			Handler: gw,
			// MaxHeaderBytes: 8192,
			// ReadTimeout:    5 * time.Second,
			// WriteTimeout:   5 * time.Second,
		}
		log.Print("starting HTTP listener on ", *flagHTTP)
		log.Fatal(s.ListenAndServe())
	}()

	go func() {
		log.Print("starting HTTPS listener on ", *flagHTTPS)
		log.Fatal(http.ListenAndServeTLS(*flagHTTPS, "cert.pem", "key.pem", gw))
	}()
}

func modeReverseProxy(upstreamURL string) {
	if strings.HasPrefix(upstreamURL, ":") {
		upstreamURL = "localhost" + upstreamURL
	}
	if !strings.Contains(upstreamURL, "://") {
		upstreamURL = "http://" + upstreamURL
	}
	if !strings.HasSuffix(upstreamURL, "/") {
		upstreamURL = upstreamURL + "/"
	}
	u, err := url.Parse(upstreamURL)
	if err == nil {
		log.Print("reverse proxy to ", u, " (max ", *flagProxyConns, " concurrent)")
		s := &rap.Server{
			Addr:    *flagRAP,
			Handler: rap.NewReverseProxy(u, *flagProxyConns),
		}
		err = s.ListenAndServe()
	}
	if err != nil {
		log.Print(err.Error())
	}
	close(stopChannel)
}

type echoHandler struct {
	requestsStarted  int64
	requestsFinished int64
	sleepTime        int
	fileHandler      http.Handler
}

func (eh *echoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// log.Print("echoHandler.ServeHTTP(): ", r)
	atomic.AddInt64(&eh.requestsStarted, 1)
	if eh.sleepTime > 0 {
		time.Sleep(time.Millisecond * time.Duration(eh.sleepTime))
	}

	if strings.HasPrefix(r.RequestURI, "/plaintext") {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", "13")
		w.Write([]byte("Hello, World!"))
		return
	}

	if strings.HasPrefix(r.RequestURI, "/html") {
		eh.fileHandler.ServeHTTP(w, r)
		return
	}

	var headers bytes.Buffer
	headers.WriteString(r.Method)
	headers.WriteRune(' ')
	headers.WriteString(r.URL.Path)
	headers.WriteString("\r\n")
	for k, vv := range r.Header {
		for _, v := range vv {
			headers.WriteString(k)
			headers.WriteString(": ")
			headers.WriteString(v)
			headers.WriteString("\r\n")
		}
	}
	headers.WriteString("\r\n")

	if r.ContentLength > -1 {
		contentLength := int64(headers.Len()) + r.ContentLength
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	}
	w.Header().Set("Content-Type", "text/plain")

	w.WriteHeader(200)
	w.Write(headers.Bytes())
	io.Copy(w, r.Body)

	/*
		w.Write([]byte("\r\n"))
		if body, err := ioutil.ReadAll(r.Body); err != nil {
			w.Write([]byte(err.Error()))
		} else {
			w.Write(body)
		}
	*/
	/*
		if err := r.Write(w); err != nil {
			log.Print("echoHandler.ServeHTTP(): r.Write(): ", err.Error())
			w.Write([]byte(fmt.Sprintf("%s %s\r\nError: %s\r\n", r.Method, r.URL.Path, err.Error())))
		}
	*/
	// atomic.AddInt64(&eh.requestsFinished, 1)
}

func modeEcho() {
	/*
		echoRouter := &httprouter.Router{}
		echoRouter.GET("/", serveHome)
		echoRouter.POST("/post", servePost)
		echoRouter.GET("/gwtest", serveGwTest)
		echoRouter.GET("/return/:retcode", serveReturn)
		echoRouter.GET("/wsecho", echoCopyFull)
	*/
	eh := &echoHandler{
		sleepTime:   *flagEchoSleep,
		fileHandler: http.FileServer(http.Dir(".")),
	}
	echoServer := &http.Server{
		Addr:    *flagHTTP,
		Handler: eh,
	}
	s := &rap.Server{
		Addr:    *flagRAP,
		Handler: eh,
	}

	go func() {
		lastRequestCounter := atomic.LoadInt64(&eh.requestsStarted)
		lastBytesWritten := s.BytesWritten()
		lastBytesRead := s.BytesRead()
		for {
			c := time.Tick(1 * time.Second)
			lastTick := time.Now()
			for currTick := range c {
				elapsed := currTick.Sub(lastTick).Nanoseconds()
				lastTick = currTick
				currRequestCounter := atomic.LoadInt64(&eh.requestsStarted)
				currBytesWritten := s.BytesWritten()
				currBytesRead := s.BytesRead()
				if lastRequestCounter != currRequestCounter {
					log.Printf("stats: Conns=%d RPS=%d MbpsIn=%d MbpsOut=%d\n",
						s.ActiveConns(),
						(currRequestCounter-lastRequestCounter)*elapsed/1000000000,
						((currBytesRead-lastBytesRead)*8/1024/1024)*elapsed/1000000000,
						((currBytesWritten-lastBytesWritten)*8/1024/1024)*elapsed/1000000000)
					lastRequestCounter = currRequestCounter
					lastBytesWritten = currBytesWritten
					lastBytesRead = currBytesRead
				}
			}
		}
	}()

	go func() {
		log.Print("starting RAP echo server on ", *flagRAP)
		log.Fatal(s.ListenAndServe())
	}()

	log.Print("starting HTTP/WebSocket echo server on ", *flagHTTP)
	log.Fatal(echoServer.ListenAndServe())
}

func modeBenchmark(addr string) {
	if !strings.Contains(addr, ":") {
		addr += defaultDispatchPort
	}
	log.Print("benchmarking ", addr)

	gw := &rap.Gateway{
		Client: rap.NewClient(addr),
	}

	maxWorkers := *flagBenchWorkers
	// let's not overflow race detector limit
	if rap.RaceEnabled() {
		if maxWorkers > 8000 {
			maxWorkers = 8000
		}
	}
	numWorkers := 0

	log.Print("executing ", maxWorkers, " HTTP requests concurrently")
	var wg sync.WaitGroup
	for numWorkers < maxWorkers {
		for _, td := range testData {
			if td.Method == "GET" {
				for _, ts := range td.TestStrings {
					if numWorkers < maxWorkers {
						numWorkers++
						wg.Add(1)
						go func(m, p string) {
							var buf bytes.Buffer
							req, err := http.NewRequest(m, p, &buf)
							if err != nil {
								log.Fatal(err.Error())
							}
							for {
								recorder := httptest.NewRecorder()
								gw.ServeHTTP(recorder, req)
								if recorder.Code >= 300 {
									statusText := recorder.Header().Get("Status")
									if statusText == "" {
										statusText = http.StatusText(recorder.Code)
									}
									bodyText, _ := recorder.Body.ReadString('\n')
									bodyText = strings.TrimSpace(bodyText)
									if bodyText != "" {
										bodyText = "  (" + bodyText + ")"
									}
									log.Print(m, " ", p, ": ", recorder.Code, " ", statusText, bodyText)
									wg.Done()
									return
								}
							}
						}(td.Method, ts)
					}
				}
			}
		}
	}
	wg.Wait()
	close(stopChannel)
}

var (
	flagPprof        = flag.Bool("pprof", false, "process profile at http://localhost:6060/debug/pprof/")
	flagProfile      = flag.Bool("profile", false, "write cpu profile to file")
	flagWorkers      = flag.Int("workers", runtime.NumCPU(), "number of worker threads")
	flagEcho         = flag.Bool("echo", false, "act as HTTP/WebSocket/RAP echo server")
	flagEchoSleep    = flag.Int("echosleep", 0, "milliseconds to sleep inside echo HTTP handler")
	flagGateway      = flag.String("gateway", "", "act as HTTP(S) -> RAP gateway for the given RAP server")
	flagBenchmark    = flag.String("benchmark", "", "act as load generator for the given RAP server")
	flagReverseProxy = flag.String("reverseproxy", "", "act as RAP reverse proxy for the given HTTP server")
	flagRAP          = flag.String("rap", defaultDispatchPort, "RAP service address")
	flagHTTP         = flag.String("http", ":8080", "HTTP service address")
	flagHTTPS        = flag.String("https", ":10443", "HTTPS service address")
	flagProxyConns   = flag.Int("proxyconns", 1000, "maximum number of reverse proxy HTTP connections")
	flagExchanges    = flag.Int("exchanges", int(rap.MaxExchangeID)+1, "maximum number of exchanges (careful!)")
	flagBenchWorkers = flag.Int("benchworkers", int(rap.MaxExchangeID)+1, "benchmark concurrent workers")
	stopChannel      = make(chan os.Signal, 1)
)

var usage = func() {
	fmt.Fprintf(os.Stderr, "usage: scgateway [flags]\n"+
		"  At least one of -echo, -gateway, -benchmark or -reverseproxy must be given.\n")
	flag.CommandLine.PrintDefaults()
}

func main() {
	flag.CommandLine.Usage = usage
	flag.Parse()

	runtime.GOMAXPROCS(*flagWorkers)

	if *flagProfile {
		defer profile.Start().Stop()
	}

	if *flagPprof {
		go func() {
			log.Print("starting HTTP pprof listener on localhost:6060")
			log.Println(http.ListenAndServe("localhost:6060", nil))
		}()
	}

	maxExchangeID := *flagExchanges - 1
	if maxExchangeID < 0 || maxExchangeID > int(rap.ProtocolMaxExchangeID) {
		maxExchangeID = int(rap.ProtocolMaxExchangeID)
	}

	rap.MaxExchangeID = rap.ExchangeID(maxExchangeID)

	signal.Notify(stopChannel, os.Interrupt, os.Kill)

	if *flagEcho {
		go modeEcho()
	} else if *flagBenchmark != "" {
		go modeBenchmark(*flagBenchmark)
	} else if *flagReverseProxy != "" {
		go modeReverseProxy(*flagReverseProxy)
	} else if *flagGateway != "" {
		go modeGateway(*flagGateway)
	} else {
		usage()
		return
	}

	log.Print(runtime.GOMAXPROCS(0), " worker threads")

	if stopSignal, ok := <-stopChannel; ok {
		log.Print(stopSignal)
	} else {
		log.Print("exiting")
	}
}
