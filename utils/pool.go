package utils

import (
	"bytes"
	"sync"
)

// Pool 泛型对象池，封装 sync.Pool 提供类型安全的 Get/Put。
// 适用于任意需要复用的对象（如 *struct、*Decoder 等）。
//
//	var decoderPool = utils.NewPool(func() *schema.Decoder {
//	    d := schema.NewDecoder()
//	    d.IgnoreUnknownKeys(true)
//	    return d
//	})
//	decoder := decoderPool.Get()
//	defer decoderPool.Put(decoder)
//
// 注意：归还前应由调用方清理对象状态，避免引用泄漏。
type Pool[T any] struct {
	pool sync.Pool
}

// NewPool 创建泛型对象池。newFn 必须返回新的实例，nil 会 panic。
func NewPool[T any](newFn func() T) *Pool[T] {
	if newFn == nil {
		panic("utils: NewPool requires non-nil constructor")
	}
	return &Pool[T]{
		pool: sync.Pool{New: func() any { return newFn() }},
	}
}

// Get 从池中获取一个对象，如果池为空则调用 newFn 创建新的。
func (p *Pool[T]) Get() T {
	return p.pool.Get().(T)
}

// Put 将对象归还到池中。
func (p *Pool[T]) Put(x T) {
	p.pool.Put(x)
}

// BufferPool 复用 *bytes.Buffer 的对象池。
// Get 时自动 Reset，调用方无需再手动 Reset。
//
//	var pool = utils.NewBufferPool()
//	buf := pool.Get()
//	defer pool.Put(buf)
//	buf.WriteString("hello")
type BufferPool struct {
	pool sync.Pool
}

// NewBufferPool 创建 *bytes.Buffer 复用池。
func NewBufferPool() *BufferPool {
	return &BufferPool{
		pool: sync.Pool{New: func() any { return new(bytes.Buffer) }},
	}
}

// Get 获取一个已 Reset 的 *bytes.Buffer。
func (p *BufferPool) Get() *bytes.Buffer {
	buf := p.pool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// Put 归还 *bytes.Buffer。nil 将被忽略。
func (p *BufferPool) Put(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	p.pool.Put(buf)
}

// BytePool 复用 *[]byte 的对象池。
// 支持最大容量限制：归还时若 cap 超过限制则丢弃，避免偶发大缓冲区常驻池中导致内存膨胀。
//
//	// 初始 cap=256，不限制归还上限
//	var pool = utils.NewBytePool(256)
//
//	// 初始 cap=4096，归还上限 64KB（超过则丢弃）
//	var pool = utils.NewBytePoolWithMax(4096, 64*1024)
//	buf := pool.Get()
//	defer pool.Put(buf)
//	*buf = append((*buf)[:0], data...)
type BytePool struct {
	pool   sync.Pool
	maxCap int // <= 0 表示不限制
}

// NewBytePool 创建 *[]byte 复用池，初始容量为 initCap。
// 归还时不限制缓冲区容量。
func NewBytePool(initCap int) *BytePool {
	if initCap < 0 {
		initCap = 0
	}
	return &BytePool{
		pool: sync.Pool{New: func() any {
			b := make([]byte, 0, initCap)
			return &b
		}},
	}
}

// NewBytePoolWithMax 创建 *[]byte 复用池，并设置归还时的容量上限。
// 若归还时 cap(*buf) > maxCap，缓冲区将被丢弃（不归还），
// 防止偶发大缓冲区常驻池中导致内存膨胀。
func NewBytePoolWithMax(initCap, maxCap int) *BytePool {
	p := NewBytePool(initCap)
	p.maxCap = maxCap
	return p
}

// Get 获取一个长度为 0 的 *[]byte（容量保持为池化时的容量）。
func (p *BytePool) Get() *[]byte {
	b := p.pool.Get().(*[]byte)
	*b = (*b)[:0]
	return b
}

// Put 归还 *[]byte。nil 或容量超过 maxCap 的缓冲区将被丢弃。
func (p *BytePool) Put(b *[]byte) {
	if b == nil {
		return
	}
	if p.maxCap > 0 && cap(*b) > p.maxCap {
		return
	}
	*b = (*b)[:0]
	p.pool.Put(b)
}

// ClearMap 清空 map 中的所有 keys，保留底层存储供复用。
// 适用于与对象池配合使用：对象从池中取出后 map 非 nil，
// 归还前调用 ClearMap 而非置 nil，避免下次使用时重新 make。
//
//	var pool = utils.NewPool(func() *MyObj {
//	    return &MyObj{Data: make(map[string]int, 8)}
//	})
//	obj := pool.Get()
//	defer pool.Put(obj)
//	// ... 使用 obj.Data ...
//	defer utils.ClearMap(obj.Data) // 在 Put 前清空
func ClearMap[M ~map[K]V, K comparable, V any](m M) {
	for k := range m {
		delete(m, k)
	}
}
