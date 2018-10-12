package rap

import (
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
)

func Benchmark_single_4k_frame_latency(b *testing.B) {
	pipedAutobahnServer(nil, func(addr string) {
		// dial the server
		u := "ws://" + addr + "/f"
		ws, _, err := websocket.DefaultDialer.Dial(u, nil)
		if err != nil {
			log.Fatalf("%v", err)
		}
		defer ws.Close()

		buf := make([]byte, 4096, 4096)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
				log.Fatalf("%v", err)
			}
			_, p, err := ws.ReadMessage()
			if err != nil {
				log.Fatalf("%v", err)
			}
			if len(p) != len(buf) {
				log.Fatalf("bad message")
			}
		}
		b.StopTimer()

		ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	})
}

func Benchmark_streamed_4k_frame_latency(b *testing.B) {
	pipedAutobahnServer(nil, func(addr string) {
		// dial the server
		u := "ws://" + addr + "/f"
		ws, _, err := websocket.DefaultDialer.Dial(u, nil)
		if err != nil {
			log.Fatalf("%v", err)
		}
		defer ws.Close()

		buf := make([]byte, 4096, 4096)

		b.ResetTimer()
		go func() {
			for i := 0; i < b.N; i++ {
				if err := ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
					log.Fatalf("%v", err)
				}
			}
		}()
		for i := 0; i < b.N; i++ {
			_, p, err := ws.ReadMessage()
			if err != nil {
				log.Fatalf("%v", err)
			}
			if len(p) != len(buf) {
				log.Fatalf("bad message")
			}
		}
		b.StopTimer()

		ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	})
}

/*
func init() {
	pprofsync.Do(func() {
		go func() {
			log.Println(http.ListenAndServe("localhost:6060", nil))
		}()
	})
}
*/

func Benchmark_100k_small_queries(b *testing.B) {
	connCount := 100000
	parallelism := 20000
	queryCount := int32(connCount)
	serveCount := int32(0)

	s := &Server{
		Addr: srvAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&serveCount, 1)
			w.WriteHeader(200)
		}),
	}
	ln, lnerr := s.Listen(srvAddr)
	if lnerr != nil {
		log.Fatal(lnerr)
	}
	defer s.Close()
	go s.Serve(ln)

	c := NewClient(s.Addr)
	defer c.Close()

	wg := sync.WaitGroup{}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for i := 0; i < parallelism; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for atomic.LoadInt32(&serveCount) < atomic.LoadInt32(&queryCount) {
					rr := httptest.NewRecorder()
					r := httptest.NewRequest("GET", "/", nil)
					c.ServeHTTP(rr, r)
				}
			}()
		}
	}
	wg.Wait()
	b.StopTimer()
	if atomic.LoadInt32(&serveCount) < atomic.LoadInt32(&queryCount) {
		log.Fatal("serveCount (", serveCount, ") < queryCount (", queryCount, ")")
	}
}
