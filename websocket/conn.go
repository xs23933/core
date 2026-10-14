package websocket

import (
	"context"
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
	conn    *websocket.Conn
	send    chan *message
	done    chan struct{}
	shard   *Shard
	meta    atomic.Value // any
	closed  atomic.Bool
	queueMu sync.Mutex
	options Options
	context context.Context
	cancel  context.CancelFunc

	// 批量发送相关
	batchMu     sync.Mutex
	batchMsgs   []*message
	batchTimer  *time.Timer
	batchConfig BatchConfig
}

func newConn(c *websocket.Conn) *Conn { return newConnWithOptions(c, Options{}, context.Background()) }
func newConnWithOptions(c *websocket.Conn, options Options, parent context.Context) *Conn {
	ctx, cancel := context.WithCancel(parent)
	if c != nil {
		if options.ReadLimit > 0 {
			c.SetReadLimit(options.ReadLimit)
		}
		if options.ReadTimeout > 0 {
			c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(options.ReadTimeout)) })
		}
	}
	return &Conn{options: options, context: ctx, cancel: cancel,
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
	if c.closed.Load() {
		c.batchMu.Unlock()
		return
	}
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
		shouldClose := c.flushBatch()
		c.batchMu.Unlock()
		if shouldClose {
			c.Close()
		}
		return
	}

	// 如果没有定时器，则创建一个
	if c.batchTimer == nil {
		c.batchTimer = time.AfterFunc(c.batchConfig.MaxDelay, func() {
			c.batchMu.Lock()
			shouldClose := c.flushBatch()
			c.batchMu.Unlock()
			if shouldClose {
				c.Close()
			}
		})
	}
	c.batchMu.Unlock()
}

func (c *Conn) flushBatch() bool {
	if len(c.batchMsgs) == 0 {
		return false
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
			return c.flushBatchSeparate()
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

	// 先撤回批量队列所有权，后续归还或入队不能再被 Close 重复处理。
	batch := c.batchMsgs
	c.batchMsgs = c.batchMsgs[:0]
	// 合并消息
	for i, msg := range batch {
		if i > 0 {
			*mergedBuf = append(*mergedBuf, '\n')
		}
		*mergedBuf = append(*mergedBuf, *msg.data...)
		putBuffer(msg.data)
	}

	// 发送合并后的消息
	return !c.sendDirectWithType(msgType, mergedBuf)
}

func (c *Conn) flushBatchSeparate() bool {
	batch := c.batchMsgs
	c.batchMsgs = c.batchMsgs[:0]
	shouldClose := false
	// 每个缓冲区只转移一次：成功入队或立即归还。
	for _, msg := range batch {
		if !c.sendDirectWithType(msg.typ, msg.data) {
			shouldClose = true
		}
	}
	return shouldClose
}

// sendDirectWithType 转移缓冲区所有权，不在持有 batchMu 时递归调用 Close。
func (c *Conn) sendDirectWithType(messageType MessageType, data *[]byte) bool {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	if c.closed.Load() {
		putBuffer(data)
		return false
	}
	msg := &message{typ: messageType, data: data}
	select {
	case c.send <- msg:
		return true
	default:
		putBuffer(data)
		return false
	}
}

// TrySend 复制消息并尝试入队；true不表示已经网络发送或对端接收。队满即关闭连接。
func (c *Conn) TrySend(data []byte) bool { return c.TrySendWithType(TextMessage, data) }

// TrySendWithType 保留消息类型，关闭或背压时返回false且归还缓冲区。
func (c *Conn) TrySendWithType(typ MessageType, data []byte) bool {
	if c.closed.Load() {
		return false
	}
	buf := bufferPool.Get()
	*buf = append(*buf, data...)
	if c.sendDirectWithType(typ, buf) {
		return true
	}
	c.Close()
	return false
}
func (c *Conn) Send(data []byte)                          { c.TrySend(data) }
func (c *Conn) SendWithType(typ MessageType, data []byte) { c.TrySendWithType(typ, data) }

// Context 只绑定连接生命周期；关闭连接时取消，不持有core.Ctx。
func (c *Conn) Context() context.Context {
	if c.context == nil {
		return context.Background()
	}
	return c.context
}

func (c *Conn) readLoop(onMessage func(*Conn, MessageType, []byte)) {
	defer c.Close()

	for {
		if c.options.ReadTimeout > 0 {
			if err := c.conn.SetReadDeadline(time.Now().Add(c.options.ReadTimeout)); err != nil {
				return
			}
		}
		messageType, reader, err := c.conn.NextReader()
		if err != nil {
			return
		}

		buf := bufferPool.Get()

		tmp := make([]byte, 512)

		for {
			n, err := reader.Read(tmp)
			if n > 0 {
				if c.options.ReadLimit > 0 && int64(len(*buf))+int64(n) > c.options.ReadLimit {
					putBuffer(buf)
					return
				}
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

		func() {
			defer putBuffer(buf)
			if onMessage != nil {
				onMessage(c, MessageType(messageType), *buf)
			}
		}()
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
			if c.options.WriteTimeout > 0 {
				if err := c.conn.SetWriteDeadline(time.Now().Add(c.options.WriteTimeout)); err != nil {
					putBuffer(msg.data)
					return
				}
			}
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
			if c.options.WriteTimeout > 0 {
				if err := c.conn.SetWriteDeadline(time.Now().Add(c.options.WriteTimeout)); err != nil {
					return
				}
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

/*
SetMeta 设置连接元数据。

@param meta 连接元数据

示例：

// 在 OnConnect 回调中设置 meta

	ws.OnConnect(func(conn *Conn) {
		// 在外层 Handler 先提取请求字段，由闭包设置连接元数据
		meta := &UserMeta{
			UserID: "123",
			Username: "john",
		}
		conn.SetMeta(meta)
	})

// 在 OnMessage 回调中获取

	ws.OnMessage(func(conn *Conn, messageType MessageType, data []byte) {
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
GetMeta 以类型安全的方式读取连接元数据。

@param c WebSocket连接
@return T 元数据
@return bool 元数据存在且类型匹配时为true

示例：

// 在 OnMessage 回调中获取

	ws.OnMessage(func(conn *Conn, messageType MessageType, data []byte) {
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
	c.queueMu.Lock()
	if !c.closed.CompareAndSwap(false, true) {
		c.queueMu.Unlock()
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		close(c.done)
	}
	// 与所有入队共享锁，关闭后不再产生无人归还的队列缓冲区。
drain:
	for {
		select {
		case msg := <-c.send:
			if msg != nil {
				putBuffer(msg.data)
			}
		default:
			break drain
		}
	}
	c.queueMu.Unlock()

	// 与批量提交、定时刷新共享锁，排空时取得剩余缓冲区的唯一所有权。
	c.batchMu.Lock()
	if c.batchTimer != nil {
		c.batchTimer.Stop()
		c.batchTimer = nil
	}
	batch := c.batchMsgs
	c.batchMsgs = nil
	c.batchMu.Unlock()
	for _, msg := range batch {
		if msg != nil {
			putBuffer(msg.data)
		}
	}

	DefaultManager.Remove(c)
	DefaultUserManager.Remove(c)
	if c.conn != nil {
		c.conn.Close()
	}
}
