package rap

import (
	"log"
	"testing"

	"github.com/gorilla/websocket"
)

func Benchmark_single_4k_frame_latency(b *testing.B) {
	pipedAutobahnServer(nil, func(addr string) {
		// Connect to the server
		u := "ws://" + addr + "/c"
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
		// Connect to the server
		u := "ws://" + addr + "/c"
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
