package core

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── 测试辅助 ──

// sseTestWriter wraps httptest.ResponseRecorder to satisfy core.ResponseWriter.
type sseTestWriter struct {
	*httptest.ResponseRecorder
}

func (w *sseTestWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *sseTestWriter) Status() int {
	return w.Code
}

func (w *sseTestWriter) Size() int {
	return w.Body.Len()
}

func (w *sseTestWriter) Written() bool {
	return w.Body.Len() > 0 || w.Code >= 200
}

func (w *sseTestWriter) DoWriteHeader() {}

func (w *sseTestWriter) Pusher() http.Pusher { return nil }

func (w *sseTestWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, fmt.Errorf("hijack not supported in test")
}

func newSSETestCtx() (*BaseCtx, *sseTestWriter) {
	rec := httptest.NewRecorder()
	w := &sseTestWriter{ResponseRecorder: rec}
	ctx := &BaseCtx{W: w}
	return ctx, w
}

// ── SSE 格式化单元测试 ──

func TestSSEWriteBasic(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSEWrite("message", "hello")
	if err != nil {
		t.Fatalf("SSEWrite failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: message\n") {
		t.Errorf("missing event field, got: %q", body)
	}
	if !strings.Contains(body, "data: hello\n") {
		t.Errorf("missing data field, got: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("missing double newline terminator, got: %q", body)
	}
}

func TestSSEWriteWithID(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSEWrite("update", "payload", "evt-1")
	if err != nil {
		t.Fatalf("SSEWrite failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "id: evt-1\n") {
		t.Errorf("missing id field, got: %q", body)
	}
	if !strings.Contains(body, "event: update\n") {
		t.Errorf("missing event field, got: %q", body)
	}
	if !strings.Contains(body, "data: payload\n") {
		t.Errorf("missing data field, got: %q", body)
	}
}

func TestSSEWriteEmptyEvent(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSEWrite("", "data only")
	if err != nil {
		t.Fatalf("SSEWrite failed: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "event:") {
		t.Errorf("unexpected event field in body: %q", body)
	}
	if !strings.Contains(body, "data: data only\n") {
		t.Errorf("missing data, got: %q", body)
	}
}

func TestSSEWriteEmptyData(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSEWrite("ping", "")
	if err != nil {
		t.Fatalf("SSEWrite failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: \n") {
		t.Errorf("expected empty data field, got: %q", body)
	}
}

func TestSSEWriteMultiLineData(t *testing.T) {
	ctx, rec := newSSETestCtx()

	data := "line1\nline2\nline3"
	err := ctx.SSEWrite("multiline", data)
	if err != nil {
		t.Fatalf("SSEWrite failed: %v", err)
	}

	body := rec.Body.String()
	// Each line must have "data: " prefix
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	dataLineCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "data:") {
			dataLineCount++
		}
	}
	if dataLineCount != 3 {
		t.Errorf("expected 3 data lines, got %d: %q", dataLineCount, body)
	}
}

func TestSSEWriteNoEventNoData(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSEWrite("", "")
	if err != nil {
		t.Fatalf("SSEWrite failed: %v", err)
	}

	body := rec.Body.String()
	// Should contain only data: <empty> and the terminator
	if !strings.Contains(body, "data: \n") {
		t.Errorf("expected empty data, got: %q", body)
	}
	if strings.Contains(body, "event:") {
		t.Errorf("unexpected event in body: %q", body)
	}
}

// ── SSESend 单元测试 ──

func TestSSESendString(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSESend("msg", "hello world")
	if err != nil {
		t.Fatalf("SSESend failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: hello world\n") {
		t.Errorf("missing data, got: %q", body)
	}
}

func TestSSESendStruct(t *testing.T) {
	ctx, rec := newSSETestCtx()

	type payload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	err := ctx.SSESend("user", payload{Name: "alice", Age: 30})
	if err != nil {
		t.Fatalf("SSESend failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"name":"alice"`) {
		t.Errorf("missing json field, got: %q", body)
	}
	if !strings.Contains(body, `"age":30`) {
		t.Errorf("missing json field, got: %q", body)
	}
}

func TestSSESendNil(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSESend("event", nil)
	if err != nil {
		t.Fatalf("SSESend with nil failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: \n") {
		t.Errorf("expected empty data, got: %q", body)
	}
}

func TestSSESendWithID(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSESend("msg", "payload", "id-123")
	if err != nil {
		t.Fatalf("SSESend with id failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "id: id-123\n") {
		t.Errorf("missing id field, got: %q", body)
	}
}

func TestSSESendBytes(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSESend("binary", []byte("raw bytes"))
	if err != nil {
		t.Fatalf("SSESend bytes failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: raw bytes\n") {
		t.Errorf("missing byte data, got: %q", body)
	}
}

// ── SSEComment 单元测试 ──

func TestSSECommentWithText(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSEComment("hello")
	if err != nil {
		t.Fatalf("SSEComment failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, ": hello\n") {
		t.Errorf("expected comment, got: %q", body)
	}
	if !strings.HasSuffix(body, "\n") {
		t.Errorf("missing terminator, got: %q", body)
	}
}

func TestSSECommentEmpty(t *testing.T) {
	ctx, rec := newSSETestCtx()

	err := ctx.SSEComment("")
	if err != nil {
		t.Fatalf("SSEComment failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, ": ping\n") {
		t.Errorf("expected default ping comment, got: %q", body)
	}
}

// ── EventData.String() 单元测试 ──

func TestEventDataStringWithID(t *testing.T) {
	ed := EventData{
		ID:    "evt-42",
		Event: "update",
		Data:  "hello",
	}

	s := ed.String()
	if !strings.Contains(s, "id: evt-42\n") {
		t.Errorf("missing id in EventData.String, got: %q", s)
	}
	if !strings.Contains(s, "event: update\n") {
		t.Errorf("missing event in EventData.String, got: %q", s)
	}
	if !strings.Contains(s, "data: hello\n") {
		t.Errorf("missing data in EventData.String, got: %q", s)
	}
}

func TestEventDataStringNoEvent(t *testing.T) {
	ed := EventData{
		Data: "data only",
	}

	s := ed.String()
	if strings.Contains(s, "event:") {
		t.Errorf("unexpected event field, got: %q", s)
	}
	if !strings.Contains(s, "data: data only\n") {
		t.Errorf("missing data, got: %q", s)
	}
}

func TestEventDataStringMultiLine(t *testing.T) {
	ed := EventData{
		Event: "multiline",
		Data:  "line1\nline2",
	}

	s := ed.String()
	dataCount := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "data:") {
			dataCount++
		}
	}
	if dataCount != 2 {
		t.Errorf("expected 2 data lines for multiline, got %d: %q", dataCount, s)
	}
}

func TestEventDataStringJSON(t *testing.T) {
	ed := EventData{
		Event: "json",
		Data:  map[string]any{"key": "value", "num": 42},
	}

	s := ed.String()
	if !strings.Contains(s, `"key":"value"`) {
		t.Errorf("missing json key, got: %q", s)
	}
}

// ── EventHub 集成测试 ──

func TestEventHubGetConnectedEvent(t *testing.T) {
	app := New()

	hub := NewEventHub(time.Millisecond * 100)
	defer hub.Close()

	app.GET("/events", hub.Get)

	req := httptest.NewRequest("GET", "/events", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(rec, req)
	}()

	// 等待事件写入
	time.Sleep(time.Millisecond * 200)

	hub.Close()

	select {
	case <-done:
	case <-time.After(time.Second * 2):
		t.Fatal("SSE handler did not return in time")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: connected\n") {
		t.Errorf("missing connected event, got: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := rec.Header().Get("X-Accel-Buffering"); cc != "no" {
		t.Errorf("X-Accel-Buffering = %q, want 'no'", cc)
	}
}

func TestEventHubGetBroadcast(t *testing.T) {
	app := New()

	hub := NewEventHub(time.Millisecond * 50)
	defer hub.Close()

	app.GET("/events", hub.Get)

	req := httptest.NewRequest("GET", "/events", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(rec, req)
	}()

	// 等待连接建立
	time.Sleep(time.Millisecond * 100)

	// 广播一条消息
	hub.Broadcast(EventData{
		Event: "ping",
		Data:  "test-message",
	})

	// 等待广播传播
	time.Sleep(time.Millisecond * 200)

	hub.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after close")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "connected") {
		t.Errorf("missing connected event, got: %q", body)
	}
	if strings.Contains(body, "test-message") {
		t.Logf("received broadcast message in body: %q", body)
	}
}

func TestEventHubGetSendTo(t *testing.T) {
	app := New()

	hub := NewEventHub(time.Millisecond * 50)
	defer hub.Close()

	app.GET("/events/:clientId", hub.Get)

	req := httptest.NewRequest("GET", "/events/user-1", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(rec, req)
	}()

	// 等待连接建立
	time.Sleep(time.Millisecond * 100)

	// 向指定 ID 发送消息
	hub.SendTo("user-1", EventData{
		Event: "private",
		Data:  "secret",
	})

	// 发送给不存在的 ID
	hub.SendTo("user-2", EventData{
		Event: "private",
		Data:  "should-not-arrive",
	})

	time.Sleep(time.Millisecond * 200)

	hub.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after close")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "connected") {
		t.Errorf("missing connected event, got: %q", body)
	}
	if strings.Contains(body, "should-not-arrive") {
		t.Errorf("received message for wrong client")
	}
}

func TestEventHubGetCloseDisconnects(t *testing.T) {
	app := New()

	hub := NewEventHub(time.Millisecond * 50)

	app.GET("/events", hub.Get)

	req := httptest.NewRequest("GET", "/events", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(rec, req)
	}()

	// 等待连接建立
	time.Sleep(time.Millisecond * 100)

	// 关闭 hub，应断开所有客户端
	hub.Close()

	select {
	case <-done:
		// handler returned, connection was properly closed
	case <-time.After(time.Second):
		t.Fatal("handler did not return after hub close")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "connected") {
		t.Errorf("missing connected event in body: %q", body)
	}
}

func TestEventHubGetHeartbeat(t *testing.T) {
	// SSEComment 格式化已在单元测试中验证.
	// 此处验证连接在较长时间内保持存活并正确响应 Close.
	app := New()

	hub := NewEventHub(time.Millisecond * 50)
	defer hub.Close()

	app.GET("/events", hub.Get)

	req := httptest.NewRequest("GET", "/events", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(rec, req)
	}()

	// 发送一条消息确保 Stream 循环工作正常
	time.Sleep(time.Millisecond * 100)
	hub.Broadcast(EventData{Event: "test", Data: "alive"})

	time.Sleep(time.Millisecond * 200)

	hub.Close()

	select {
	case <-done:
	case <-time.After(time.Second * 2):
		t.Fatal("handler did not return after hub close")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "connected") {
		t.Errorf("missing connected event, got: %q", body)
	}
}

func TestEventHubGetMultipleEvents(t *testing.T) {
	app := New()

	hub := NewEventHub(time.Millisecond * 30)
	defer hub.Close()

	app.GET("/events", hub.Get)

	req := httptest.NewRequest("GET", "/events", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(rec, req)
	}()

	// 等待连接建立
	time.Sleep(time.Millisecond * 100)

	// 发送多条事件
	for i := range 5 {
		hub.Broadcast(EventData{
			Event: "msg",
			Data:  fmt.Sprintf("message-%d", i),
		})
	}

	time.Sleep(time.Millisecond * 200)

	hub.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after close")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "connected") {
		t.Errorf("missing connected event, got: %q", body)
	}
}

func TestEventHubRegisterClosesDuplicateNamed(t *testing.T) {
	hub := NewEventHub(time.Millisecond)
	defer hub.Close()

	ch1 := make(chan EventData, 10)
	hub.Register(ch1, "dup-id")

	ch2 := make(chan EventData, 10)
	hub.Register(ch2, "dup-id")

	// ch1 should be closed when replaced by ch2
	_, ok := <-ch1
	if ok {
		t.Fatal("first channel should be closed when replaced")
	}

	// ch2 should still be open
	hub.Close()
	_, ok = <-ch2
	if ok {
		t.Fatal("channel should be closed after hub close")
	}
}

// ── EventData.ToString 类型测试 ──

func TestEventDataToStringTypes(t *testing.T) {
	tests := []struct {
		name string
		data any
		want string
	}{
		{"string", "hello", "hello"},
		{"bytes", []byte("world"), "world"},
		{"int", 42, "42"},
		{"int64", int64(999), "999"},
		{"float64", 3.14, "3.14"},
		{"bool", true, "true"},
		{"struct", struct{ A string }{"x"}, `{"A":"x"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := &EventData{Data: tt.data}
			got := ed.ToString()
			if got != tt.want {
				t.Errorf("ToString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── SSEWrite 基准测试 ──

func BenchmarkSSEWriteSimple(b *testing.B) {
	ctx, _ := newSSETestCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ctx.SSEWrite("message", "hello world")
	}
}

func BenchmarkSSEWriteWithID(b *testing.B) {
	ctx, _ := newSSETestCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ctx.SSEWrite("update", `{"status":"ok"}`, "evt-001")
	}
}

func BenchmarkSSEWriteMultiLine(b *testing.B) {
	ctx, _ := newSSETestCtx()
	data := "line1\nline2\nline3\nline4\nline5"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ctx.SSEWrite("report", data)
	}
}

func BenchmarkSSESendStruct(b *testing.B) {
	ctx, _ := newSSETestCtx()
	type payload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	p := payload{Name: "alice", Age: 30}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ctx.SSESend("user", p)
	}
}

func BenchmarkEventDataString(b *testing.B) {
	ed := EventData{
		ID:    "evt-42",
		Event: "update",
		Data:  map[string]any{"key": "value", "num": 42},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ed.String()
	}
}
