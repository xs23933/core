package core

import (
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
)

type EventData struct {
	ID    string `json:"id,omitempty"`
	Event string `json:"event"`
	Data  any    `json:"data"`
}

func (h EventData) String() string {
	var buf strings.Builder

	if h.ID != "" {
		buf.WriteString("id: ")
		buf.WriteString(h.ID)
		buf.WriteByte('\n')
	}
	if h.Event != "" {
		buf.WriteString("event: ")
		buf.WriteString(h.Event)
		buf.WriteByte('\n')
	}
	data := h.ToString()
	if data != "" {
		for _, line := range splitLines(data) {
			buf.WriteString("data: ")
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	} else {
		buf.WriteString("data: \n")
	}
	buf.WriteByte('\n')
	return buf.String()
}

func (h *EventData) ToString() string {
	switch v := h.Data.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int, int8, int16, int32, int64, Int:
		return fmt.Sprint(v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(v)
	case float32, float64:
		return fmt.Sprint(v)
	default:
		dat, _ := sonic.MarshalString(v)
		return dat
	}
}

type EventHub struct {
	mu           sync.Mutex
	clients      atomic.Value // map[chan EventData]struct{}, copy-on-write（匿名模式）
	namedClients atomic.Value // map[string]chan EventData, copy-on-write（ID 模式）
	queue        chan EventData
	interval     time.Duration
	stop         chan struct{}
	closed       atomic.Bool
}

func NewEventHub(interval ...time.Duration) *EventHub {
	itv := time.Millisecond * 1000
	if len(interval) > 0 {
		itv = interval[0]
	}
	h := &EventHub{
		queue:    make(chan EventData, 1000),
		interval: itv,
		stop:     make(chan struct{}),
	}
	h.storeClients(make(map[chan EventData]struct{}))
	h.storeNamedClients(make(map[string]chan EventData))

	go h.start()
	return h
}

func (h *EventHub) loadClients() map[chan EventData]struct{} {
	if clients, ok := h.clients.Load().(map[chan EventData]struct{}); ok && clients != nil {
		return clients
	}
	return nil
}

func (h *EventHub) storeClients(clients map[chan EventData]struct{}) {
	h.clients.Store(clients)
}

func (h *EventHub) loadNamedClients() map[string]chan EventData {
	if named, ok := h.namedClients.Load().(map[string]chan EventData); ok && named != nil {
		return named
	}
	return nil
}

func (h *EventHub) storeNamedClients(named map[string]chan EventData) {
	h.namedClients.Store(named)
}

func (h *EventHub) start() {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	var buffer []EventData
	for {
		select {
		case <-h.stop:
			return
		case data, ok := <-h.queue:
			if !ok {
				return
			}
			buffer = append(buffer, data)
		case <-ticker.C:
			if len(buffer) == 0 {
				continue
			}

			if len(buffer) == 1 {
				h.broadcast(buffer[0])
			} else {
				// 打包成一个批次事件
				batch := EventData{
					Event: "batch",
					Data:  buffer,
				}
				h.broadcast(batch)
			}
			buffer = nil
		}
	}
}

func (h *EventHub) Register(c chan EventData, id ...string) {
	if h.closed.Load() {
		close(c)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed.Load() {
		close(c)
		return
	}

	if len(id) > 0 && id[0] != "" {
		named := h.loadNamedClients()
		if old, ok := named[id[0]]; ok {
			if old == c {
				return
			}
			clients := h.loadClients()
			next := make(map[chan EventData]struct{}, len(clients)-1)
			for ch := range clients {
				if ch != old {
					next[ch] = struct{}{}
				}
			}
			h.storeClients(next)
			close(old)
		}
		nextNamed := make(map[string]chan EventData, len(named)+1)
		maps.Copy(nextNamed, named)
		nextNamed[id[0]] = c
		h.storeNamedClients(nextNamed)
	}

	clients := h.loadClients()
	next := make(map[chan EventData]struct{}, len(clients)+1)
	for ch := range clients {
		next[ch] = struct{}{}
	}
	next[c] = struct{}{}
	h.storeClients(next)
}

func (h *EventHub) UnRegister(c chan EventData, id ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(id) > 0 && id[0] != "" {
		named := h.loadNamedClients()
		if ch, ok := named[id[0]]; ok {
			nextNamed := make(map[string]chan EventData, len(named)-1)
			for k, v := range named {
				if k != id[0] {
					nextNamed[k] = v
				}
			}
			h.storeNamedClients(nextNamed)

			clients := h.loadClients()
			if _, ok := clients[ch]; ok {
				nextClients := make(map[chan EventData]struct{}, len(clients)-1)
				for clientCh := range clients {
					if clientCh != ch {
						nextClients[clientCh] = struct{}{}
					}
				}
				h.storeClients(nextClients)
			}
			close(ch)
			return
		}
	}

	clients := h.loadClients()
	if _, ok := clients[c]; !ok {
		return
	}
	next := make(map[chan EventData]struct{}, len(clients)-1)
	for ch := range clients {
		if ch != c {
			next[ch] = struct{}{}
		}
	}
	h.storeClients(next)

	named := h.loadNamedClients()
	for k, v := range named {
		if v == c {
			nextNamed := make(map[string]chan EventData, len(named)-1)
			for nk, nv := range named {
				if nk != k {
					nextNamed[nk] = nv
				}
			}
			h.storeNamedClients(nextNamed)
			break
		}
	}

	close(c)
}

func (h *EventHub) broadcast(data EventData) {
	for c := range h.loadClients() {
		select {
		case c <- data:
			continue
		default:
		}
	}
}

func (h *EventHub) Broadcast(data EventData) {
	if h.closed.Load() {
		return
	}
	select {
	case h.queue <- data:
		return
	case <-h.stop:
		return
	default:
	}
}

func (h *EventHub) Close() {
	if !h.closed.CompareAndSwap(false, true) {
		return
	}
	close(h.stop)

	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.loadClients()
	for ch := range clients {
		close(ch)
	}
	h.storeClients(make(map[chan EventData]struct{}))
	h.storeNamedClients(make(map[string]chan EventData))
}

func (h *EventHub) Get(c Ctx) {
	c.SetHeader("Content-Type", "text/event-stream;charset=utf-8")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")
	c.SetHeader("X-Accel-Buffering", "no") // 禁用 nginx 缓冲

	ch := make(chan EventData, 100)
	id := c.Param("param")
	if id == "" {
		id = c.Query("id")
	}
	h.Register(ch, id)
	defer h.UnRegister(ch, id)

	// 发送连接成功事件
	c.SSEWrite("connected", "{}")

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	c.Stream(func(w io.Writer) bool {
		select {
		case msg, ok := <-ch:
			if !ok {
				return false
			}
			if err := c.SSESend(msg.Event, msg.Data, msg.ID); err != nil {
				return false
			}
		case <-heartbeat.C:
			if err := c.SSEComment("ping"); err != nil {
				return false
			}
		}
		return true
	})
}

func (h *EventHub) PostData(c Ctx) {
	data := EventData{}
	if err := c.ReadBody(&data); err != nil {
		c.ToJSON(nil, err)
		return
	}
	if data.ID != "" {
		go h.SendTo(data.ID, data)
	} else {
		go h.Broadcast(data)
	}
	c.ToJSON(data, nil)
}

// SendTo 向指定 ID 的客户端发送通知，非阻塞
func (h *EventHub) SendTo(id string, data EventData) {
	if h.closed.Load() {
		return
	}
	named := h.loadNamedClients()
	c, ok := named[id]
	if !ok {
		return
	}
	select {
	case c <- data:
	default:
	}
}
