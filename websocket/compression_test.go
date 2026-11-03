package websocket

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/gorilla/websocket"
	"github.com/xs23933/core/v3/middleware/compress"
)

func TestMessageCompressionNegotiation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, clientEnabled := range []bool{false, true} {
			for _, accept := range []string{"br,gzip,zstd", "*;q=0"} {
				w := New().WithOptions(Options{EnableCompression: enabled, ReadLimit: 1 << 20, WriteTimeout: time.Second}).OnMessage(func(c *Conn, typ MessageType, data []byte) { c.SendWithType(typ, data) })
				server := wsTestServer(t, w, compress.New(compress.Config{EnableSSE: true}))
				dialer := wire.Dialer{EnableCompression: clientEnabled, HandshakeTimeout: 2 * time.Second}
				conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", http.Header{"Accept-Encoding": []string{accept}})
				if err != nil {
					t.Fatal(err)
				}
				negotiated := strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate")
				if negotiated != (enabled && clientEnabled) || response.Header.Get("Content-Encoding") != "" {
					t.Fatal(enabled, clientEnabled, response.Header)
				}
				for _, typ := range []int{wire.TextMessage, wire.BinaryMessage} {
					payload := []byte(strings.Repeat("streamed websocket message;", 1000))
					writer, err := conn.NextWriter(typ)
					if err != nil {
						t.Fatal(err)
					}
					for i := 0; i < len(payload); i += 100 {
						end := i + 100
						if end > len(payload) {
							end = len(payload)
						}
						if _, err := writer.Write(payload[i:end]); err != nil {
							t.Fatal(err)
						}
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
						t.Fatal(err)
					}
					gotType, got, err := conn.ReadMessage()
					if err != nil || gotType != typ || string(got) != string(payload) {
						t.Fatalf("type=%d length=%d err=%v", gotType, len(got), err)
					}
				}
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestCompressedMessagesRespectDecodedReadLimit(t *testing.T) {
	var delivered atomic.Int32
	w := New().WithOptions(Options{EnableCompression: true, ReadLimit: 1024}).OnMessage(func(*Conn, MessageType, []byte) { delivered.Add(1) })
	server := wsTestServer(t, w)
	dialer := wire.Dialer{EnableCompression: true, HandshakeTimeout: time.Second}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		t.Fatal(response.Header)
	}
	if err := conn.WriteMessage(wire.TextMessage, []byte(strings.Repeat("x", 32<<10))); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("oversize decoded message was accepted")
	} else {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			t.Fatal("server failed to close an oversized compressed message")
		}
	}
	if delivered.Load() != 0 {
		t.Fatal("oversize message reached callback")
	}
}
