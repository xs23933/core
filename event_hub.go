package core

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
)

type EventData struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

func (h EventData) String() string {
	if h.Event == "" {
		return fmt.Sprintf("data: %v\n\n", h.ToString())
	}
	return fmt.Sprintf("event: %s\ndata: %v\n\n", h.Event, h.ToString())
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
	mu       sync.Mutex
	clients  atomic.Value // map[chan EventData]struct{}, copy-on-write
	queue    chan EventData
	interval time.Duration
}

func NewEventHub(interval ...time.Duration) *EventHub {
	itv := time.Millisecond * 1000
	if len(interval) > 0 {
		itv = interval[0]
	}
	h := &EventHub{
		queue:    make(chan EventData, 1000), // 缓存,避免阻塞
		interval: itv,
	}
	h.storeClients(make(map[chan EventData]struct{}))

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

func (h *EventHub) start() {
	Info("event hub run with %ds batch interval", h.interval.Seconds())
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	var buffer []EventData
	for {
		select {
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

func (h *EventHub) Register(c chan EventData) {
	h.mu.Lock()
	clients := h.loadClients()
	next := make(map[chan EventData]struct{}, len(clients)+1)
	for ch := range clients {
		next[ch] = struct{}{}
	}
	next[c] = struct{}{}
	h.storeClients(next)
	h.mu.Unlock()
}

func (h *EventHub) UnRegister(c chan EventData) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
	select {
	case h.queue <- data:
		return
	default:
	}
}

func (h *EventHub) Get(c Ctx) {
	c.SetHeader("Content-Type", "text/event-stream;charset=utf-8")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")

	ch := make(chan EventData, 100)
	h.Register(ch)
	defer h.UnRegister(ch)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	c.Stream(func(w io.Writer) bool {
		_, _ = fmt.Fprint(w, "event: touch\ndata: hi\n\n")
		return false
	})

	c.Stream(func(w io.Writer) bool {
		select {
		case msg, ok := <-ch:
			if !ok {
				return false
			}
			if _, err := fmt.Fprint(w, msg.String()); err != nil {
				return false
			}
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ":\n\n"); err != nil { // SSE 心跳标识 ping
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
	go h.Broadcast(data)
	c.ToJSON(data, nil)
}
