package rap

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
)

func (e *Exchange) proxyWebsocketWrites(buf *bufio.ReadWriter) {
	io.Copy(e, buf)
}

func (e *Exchange) proxyWebsocketReads(done chan struct{}, buf *bufio.ReadWriter) {
	for !e.hasReceivedFinal() {
		if err := e.readFrame(); err != nil {
			log.Fatal(err.Error())
			return
		}
		if err := e.fp.ProxyBody(buf); err != nil {
			log.Print("Exchange.proxyWebsocketReads() ProxyBody() ", err.Error())
			return
		}
	}
}

func (e *Exchange) initiateWebsocket(conn net.Conn, buf *bufio.ReadWriter, r *http.Request) {
	log.Fatal("Exchange.initiateWebsocket not implemented")
	/*

		// send the request
		e.WriteRequest(r)
		e.Flush()

		// read the response
		e.readFrame()
		fd := e.fdr
		defer FrameDataFree(fd)
		fr := NewFrameReader(fd)
		if fd.Header().HasHead() {
			w := httptest.NewRecorder()
			fr.ProxyResponse(w)
			resp := &http.Response{
				StatusCode: w.Code,
				ProtoMajor: 1,
				ProtoMinor: 1,
				Request:    r,
				Header:     w.Header(),
			}
			if err := resp.Write(buf); err != nil {
				log.Print(err.Error())
				return
			}
			if err := buf.Flush(); err != nil {
				log.Print(err.Error())
				return
			}
			if resp.StatusCode == 101 &&
				strings.ToLower(resp.Header.Get("Upgrade")) == "websocket" &&
				strings.ToLower(resp.Header.Get("Connection")) == "upgrade" {
				// accepted
				conn.SetDeadline(time.Time{})
				done := make(chan struct{})
				go e.proxyWebsocketReads(done, buf)
				e.proxyWebsocketWrites(buf)
				close(done)
			} else {
				if resp.Status == "" {
					resp.Status = resp.Header.Get("Status")
					if resp.Status == "" {
						resp.Status = http.StatusText(resp.StatusCode)
					}
				}
				log.Print("Exchange.serveWebsocket(): rejected by server: ", resp.StatusCode, " ", resp.Status)
			}
		}
	*/
}
