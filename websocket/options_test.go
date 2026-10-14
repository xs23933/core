package websocket

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/gorilla/websocket"
	"github.com/xs23933/core/v3"
)

func wsTestServer(t *testing.T, w *WS) *httptest.Server {
	t.Helper()
	var handlers sync.WaitGroup
	var mu sync.Mutex
	active := map[*Conn]bool{}
	connect, closeCallback := w.onConnect, w.onClose
	w.OnConnect(func(c *Conn) {
		mu.Lock()
		active[c] = true
		mu.Unlock()
		if connect != nil {
			connect(c)
		}
	})
	w.OnClose(func(c *Conn) {
		defer func() { mu.Lock(); delete(active, c); mu.Unlock() }()
		if closeCallback != nil {
			closeCallback(c)
		}
	})
	app := core.New()
	app.GET("/ws", w.Handler())
	s := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		app.ServeHTTP(rw, r)
	}))
	t.Cleanup(func() {
		s.Close()
		mu.Lock()
		connections := make([]*Conn, 0, len(active))
		for c := range active {
			connections = append(connections, c)
		}
		mu.Unlock()
		for _, c := range connections {
			c.Close()
		}
		handlers.Wait()
	})
	return s
}
func TestOptionsRequireProtocolAndOrigin(t *testing.T) {
	w := New().WithOptions(Options{Subprotocols: []string{"test.v1"}, RequireSubprotocol: true, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "https://allowed.test" }})
	s := wsTestServer(t, w)
	url := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"
	for _, tc := range []struct {
		protocol, origin string
		ok               bool
	}{{"", "https://allowed.test", false}, {"other", "https://allowed.test", false}, {"test.v1", "https://bad.test", false}, {"test.v1", "https://allowed.test", true}} {
		d := wire.Dialer{Subprotocols: []string{tc.protocol}}
		c, r, e := d.Dial(url, http.Header{"Origin": []string{tc.origin}})
		if r != nil {
			r.Body.Close()
		}
		if e == nil {
			defer c.Close()
		}
		if (e == nil) != tc.ok {
			t.Fatalf("protocol=%q origin=%q err=%v", tc.protocol, tc.origin, e)
		}
		if e == nil && c.Subprotocol() != "test.v1" {
			t.Fatal("协议未协商")
		}
	}
}
func TestTrySendCloseRaceDrainsQueue(t *testing.T) {
	for range 100 {
		c := newConn(nil)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 100 {
					c.TrySend([]byte("payload"))
				}
			}()
		}
		wg.Add(1)
		go func() { defer wg.Done(); c.Close() }()
		wg.Wait()
		if c.TrySend([]byte("closed")) {
			t.Fatal("关闭后接受消息")
		}
		if len(c.send) != 0 {
			t.Fatal("关闭队列未归还缓冲区")
		}
	}
}
func TestReadLimitClosesConnectionAndRunsCloseOnce(t *testing.T) {
	closed := make(chan struct{}, 2)
	messages := make(chan struct{}, 1)
	s := wsTestServer(t, New().WithOptions(Options{ReadLimit: 32}).OnMessage(func(*Conn, MessageType, []byte) { messages <- struct{}{} }).OnClose(func(*Conn) { closed <- struct{}{} }))
	d := wire.Dialer{WriteBufferSize: 8}
	c, _, e := d.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	writer, e := c.NextWriter(wire.TextMessage)
	if e != nil {
		t.Fatal(e)
	}
	for range 10 {
		if _, e = writer.Write([]byte("fragment")); e != nil {
			break
		}
	}
	writer.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e = c.ReadMessage(); e == nil {
		t.Fatal("累计超限未关闭")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("缺少OnClose")
	}
	select {
	case <-messages:
		t.Fatal("超限消息进入回调")
	default:
	}
	select {
	case <-closed:
		t.Fatal("OnClose重复")
	default:
	}
}
func TestReadTimeoutClosesIdleAndCancelsContext(t *testing.T) {
	connected := make(chan *Conn, 1)
	closed := make(chan struct{}, 1)
	s := wsTestServer(t, New().WithOptions(Options{ReadTimeout: 30 * time.Millisecond}).OnConnect(func(c *Conn) { connected <- c }).OnClose(func(*Conn) { closed <- struct{}{} }))
	c, _, e := wire.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	serverConn := <-connected
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("空闲读未超时")
	}
	select {
	case <-serverConn.Context().Done():
	default:
		t.Fatal("连接context未取消")
	}
}
func TestWriteTimeoutAndLocalCloseRunCleanupOnce(t *testing.T) {
	connected := make(chan *Conn, 1)
	closed := make(chan struct{}, 2)
	s := wsTestServer(t, New().WithOptions(Options{WriteTimeout: 30 * time.Millisecond}).OnConnect(func(c *Conn) { connected <- c }).OnClose(func(*Conn) { closed <- struct{}{} }))
	client, _, err := wire.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-connected
	if conn, ok := client.UnderlyingConn().(*net.TCPConn); ok {
		if err = conn.SetReadBuffer(1024); err != nil {
			t.Fatal(err)
		}
	}
	if conn, ok := server.conn.UnderlyingConn().(*net.TCPConn); ok {
		if err = conn.SetWriteBuffer(1024); err != nil {
			t.Fatal(err)
		}
	}
	if !server.TrySend(make([]byte, 8<<20)) {
		t.Fatal("消息未入队")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		server.Close()
		t.Fatal("慢客户端未因写超时关闭")
	}
	server.Close()
	server.Close()
	select {
	case <-closed:
		t.Fatal("清理回调重复")
	default:
	}
	if len(server.send) != 0 {
		t.Fatal("关闭后发送缓存残留")
	}
}
func TestOptionsCopyAndDefaultWithoutMessageCallback(t *testing.T) {
	protocols := []string{"v1"}
	w := New().WithOptions(Options{Subprotocols: protocols})
	protocols[0] = "changed"
	s := wsTestServer(t, w)
	d := wire.Dialer{Subprotocols: []string{"v1"}}
	client, _, err := d.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/ws", http.Header{"Origin": []string{"https://other.test"}})
	if err != nil {
		t.Fatal("默认来源行为改变", err)
	}
	defer client.Close()
	if client.Subprotocol() != "v1" {
		t.Fatal("协议配置被外部切片修改")
	}
	if err = client.WriteMessage(wire.TextMessage, []byte("no callback")); err != nil {
		t.Fatal(err)
	}
}
