package websocket

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xs23933/core/v3/utils"
)

type MessageType int

const (
	TextMessage   MessageType = websocket.TextMessage
	BinaryMessage MessageType = websocket.BinaryMessage
)

type message struct {
	typ  MessageType
	data *[]byte
}

const maxPooledBufferCap = 64 << 10

// bufferPool 复用 *[]byte，归还时若容量超过 maxPooledBufferCap 则丢弃，
// 避免偶发大消息常驻池中导致内存膨胀。
var bufferPool = utils.NewBytePoolWithMax(4096, maxPooledBufferCap)

func putBuffer(buf *[]byte) {
	bufferPool.Put(buf)
}

type BatchConfig struct {
	Enabled    bool          // 是否启用批量
	MaxSize    int           // 最大批量大小
	MaxDelay   time.Duration // 最大延迟
	InitialCap int           // 初始容量
}

type Conn struct {
	conn   *websocket.Conn
	send   chan *message
	done   chan struct{}
	shard  *Shard
	meta   atomic.Value // any
	closed atomic.Bool

	// 批量发送相关
	batchMu     sync.Mutex
	batchMsgs   []*message
	batchTimer  *time.Timer
	batchConfig BatchConfig
}

func newConn(c *websocket.Conn) *Conn {
	return &Conn{
		conn: c,
		send: make(chan *message, 32),
		done: make(chan struct{}),

		batchMsgs: make([]*message, 0, 10), // 预分配容量
		batchConfig: BatchConfig{
			Enabled:    false,                 // 默认关闭
			MaxSize:    10,                    // 默认最大批量大小
			MaxDelay:   50 * time.Millisecond, // 默认最大延迟
			InitialCap: 10,                    // 初始容量
		},
	}
}

func (c *Conn) EnableBatch(config BatchConfig) {
	c.batchMu.Lock()
	defer c.batchMu.Unlock()

	c.batchConfig = config
	c.batchConfig.Enabled = true
	if config.InitialCap > 0 {
		c.batchMsgs = make([]*message, 0, config.InitialCap)
	}
}

func (c *Conn) SendBatchWithType(messageType MessageType, data []byte) {
	if c.closed.Load() {
		return
	}

	c.batchMu.Lock()
	if !c.batchConfig.Enabled {
		c.batchMu.Unlock()
		c.SendWithType(messageType, data)
		return
	}

	buf := bufferPool.Get()
	*buf = append(*buf, data...)

	c.batchMsgs = append(c.batchMsgs, &message{
		typ:  messageType,
		data: buf,
	})

	// 如果达到批量大小或延迟时间，发送批量消息
	if len(c.batchMsgs) >= c.batchConfig.MaxSize {
		c.flushBatch()
		c.batchMu.Unlock()
		return
	}

	// 如果没有定时器，则创建一个
	if c.batchTimer == nil {
		c.batchTimer = time.AfterFunc(c.batchConfig.MaxDelay, func() {
			c.batchMu.Lock()
			defer c.batchMu.Unlock()
			c.flushBatch()
		})
	}
	c.batchMu.Unlock()
}

func (c *Conn) flushBatch() {
	if len(c.batchMsgs) == 0 {
		return
	}

	// 停止定时器
	if c.batchTimer != nil {
		c.batchTimer.Stop()
		c.batchTimer = nil
	}

	msgType := c.batchMsgs[0].typ
	for _, msg := range c.batchMsgs[1:] {
		if msg.typ != msgType {
			// 类型不一致，分别发送
			c.flushBatchSeparate()
			return
		}
	}

	// 计算总大小，避免多次扩容
	totalSize := 0
	for _, msg := range c.batchMsgs {
		totalSize += len(*msg.data) + 1 // +1 for separator
	}

	// 从池中获取合并缓冲区
	mergedBuf := bufferPool.Get()

	// 确保容量足够
	if cap(*mergedBuf) < totalSize {
		// 如果池中的缓冲区不够大，创建新的
		// 注意：这里不归还，因为会扩容
		newBuf := make([]byte, 0, totalSize)
		putBuffer(mergedBuf)
		mergedBuf = &newBuf
	}

	// 合并消息
	for i, msg := range c.batchMsgs {
		if i > 0 {
			*mergedBuf = append(*mergedBuf, '\n')
		}
		*mergedBuf = append(*mergedBuf, *msg.data...)
		putBuffer(msg.data)
	}

	// 发送合并后的消息
	c.sendDirectWithType(msgType, mergedBuf)

	// 清空批量队列（重用底层数组）
	c.batchMsgs = c.batchMsgs[:0]
}

func (c *Conn) flushBatchSeparate() {
	// 分别发送每条消息
	for _, msg := range c.batchMsgs {
		c.sendDirectWithType(msg.typ, msg.data)
	}
	c.batchMsgs = c.batchMsgs[:0]
}

func (c *Conn) sendDirectWithType(messageType MessageType, data *[]byte) {
	if c.closed.Load() {
		putBuffer(data)
		return
	}
	msg := &message{
		typ:  MessageType(messageType),
		data: data,
	}
	select {
	case c.send <- msg:
	default:
		putBuffer(data)
		c.Close()
	}
}

func (c *Conn) Send(data []byte) {
	c.SendWithType(websocket.TextMessage, data)
}

func (c *Conn) SendWithType(messageType MessageType, data []byte) {
	if c.closed.Load() {
		return
	}
	buf := bufferPool.Get()
	*buf = append(*buf, data...)

	msg := &message{
		typ:  messageType,
		data: buf,
	}

	select {
	case c.send <- msg:
	default:
		putBuffer(buf)
		c.Close()
	}
}

func (c *Conn) readLoop(onMessage func(*Conn, MessageType, []byte)) {
	defer c.Close()

	for {
		messageType, reader, err := c.conn.NextReader()
		if err != nil {
			return
		}

		buf := bufferPool.Get()

		tmp := make([]byte, 512)

		for {
			n, err := reader.Read(tmp)
			if n > 0 {
				*buf = append(*buf, tmp[:n]...)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				putBuffer(buf)
				return
			}
		}

		onMessage(c, MessageType(messageType), *buf)

		putBuffer(buf)
	}
}

func (c *Conn) writeLoop() {

	ticker := time.NewTicker(30 * time.Second)

	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case msg := <-c.send:
			writer, err := c.conn.NextWriter(int(msg.typ))
			if err != nil {
				putBuffer(msg.data)
				return
			}

			_, err = writer.Write(*msg.data)
			if err != nil {
				writer.Close()
				putBuffer(msg.data)
				return
			}

			err = writer.Close()
			if err != nil {
				putBuffer(msg.data)
				return
			}
			putBuffer(msg.data)

		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

/*
SetMeta sets the metadata for the connection

@param meta any

e.g:

// 在 OnConnect 回调中设置 meta

	ws.OnConnect(func(conn *websocket.Conn) {
		// 可以从 request 中获取用户信息，这里需要修改 Handler 传递用户信息
		meta := &UserMeta{
			UserID: "123",
			Username: "john",
		}
		conn.SetMeta(meta)
	})

// 在 OnMessage 回调中获取

	ws.OnMessage(func(conn *websocket.Conn, messageType int, data []byte) {
		if meta, ok := conn.GetMeta().(*UserMeta); ok {
			userID := meta.UserID
			// 处理消息
		}
	})
*/
func (c *Conn) SetMeta(meta any) {
	c.meta.Store(meta)
}

func (c *Conn) GetMeta() any {
	return c.meta.Load()
}

/*
GetMeta retrieves the metadata of the connection in a type-safe manner

@param c *Conn the websocket connection
@return T the metadata value
@return bool whether the metadata exists and is of type T

e.g:

// 在 OnMessage 回调中获取

	ws.OnMessage(func(conn *websocket.Conn, messageType int, data []byte) {
		if userID, ok := websocket.GetMeta[string](conn); ok {
			// 处理消息
		}
	})
*/
func GetMeta[T any](c *Conn) (T, bool) {
	var zero T
	v := c.meta.Load()
	if v == nil {
		return zero, false
	}
	if meta, ok := v.(T); ok {
		return meta, true
	}
	return zero, false
}

func (c *Conn) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}

	// 清理批量缓冲区 — 不用锁，CAS 保证了 Close 只执行一次
	if c.batchTimer != nil {
		c.batchTimer.Stop()
		c.batchTimer = nil
	}
	for _, msg := range c.batchMsgs {
		if msg != nil {
			putBuffer(msg.data)
		}
	}
	c.batchMsgs = nil

	DefaultManager.Remove(c)
	DefaultUserManager.Remove(c)
	if c.conn != nil {
		c.conn.Close()
	}
	if c.done != nil {
		close(c.done)
	}
}
