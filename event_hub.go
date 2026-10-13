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
	mu           sync.RWMutex
	clients      atomic.Value // map[chan EventData]struct{}, 写时复制（匿名模式）
	namedClients atomic.Value // map[string]chan EventData, 写时复制（ID 模式）
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

// Register 将管道的发送和关闭所有权交给 Hub；调用方只接收，不得自行关闭或复用已退役管道。
// 同一活动管道重复注册幂等；一个管道的多个 ID 别名共享同一生命周期。
func (h *EventHub) Register(c chan EventData, id ...string) {
	if c == nil {
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
			h.removeClientLocked(old)
		}
		nextNamed := maps.Clone(h.loadNamedClients())
		nextNamed[id[0]] = c
		h.storeNamedClients(nextNamed)
	}
	clients := h.loadClients()
	if _, ok := clients[c]; ok {
		return
	}
	next := maps.Clone(clients)
	next[c] = struct{}{}
	h.storeClients(next)
}

// UnRegister 仅清理对应管道；旧连接退出不能移除同 ID 的新连接。
func (h *EventHub) UnRegister(c chan EventData, id ...string) {
	if c == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(id) > 0 && id[0] != "" && h.loadNamedClients()[id[0]] != c {
		return
	}
	h.removeClientLocked(c)
}

// removeClientLocked 在同一临界区撤回全部别名并关闭唯一的活动管道。
func (h *EventHub) removeClientLocked(c chan EventData) {
	clients := h.loadClients()
	if _, ok := clients[c]; !ok {
		return
	}
	next := maps.Clone(clients)
	delete(next, c)
	h.storeClients(next)
	named := h.loadNamedClients()
	nextNamed := maps.Clone(named)
	for id, ch := range named {
		if ch == c {
			delete(nextNamed, id)
		}
	}
	h.storeNamedClients(nextNamed)
	close(c)
}

func (h *EventHub) broadcast(data EventData) {
	// 发送与关闭共享锁，防止快照中的管道在发送前被关闭。
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed.Load() {
		return
	}
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
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed.CompareAndSwap(false, true) {
		return
	}
	close(h.stop)
	for ch := range h.loadClients() {
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
	h.mu.RLock()
	defer h.mu.RUnlock()
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
