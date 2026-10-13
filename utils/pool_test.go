package utils

import (
	"bytes"
	"sync"
	"testing"
)

func TestPoolGetPut(t *testing.T) {
	type item struct {
		Name string
		Age  int
	}
	p := NewPool(func() *item {
		return &item{Name: "default"}
	})

	got := p.Get()
	if got == nil || got.Name != "default" {
		t.Fatalf("Get returned %v, want &item{Name:default}", got)
	}
	got.Name = "modified"
	p.Put(got)

	// 再次 Get 可能拿到同一个对象（池非空时），验证类型正确
	got2 := p.Get()
	if got2 == nil {
		t.Fatal("second Get returned nil")
	}
	p.Put(got2)
}

func TestPoolNewCalledWhenEmpty(t *testing.T) {
	calls := 0
	p := NewPool(func() *int {
		calls++
		v := 42
		return &v
	})
	// 池为空时调用 New
	got := p.Get()
	if *got != 42 {
		t.Fatalf("Get = %d, want 42", *got)
	}
	if calls != 1 {
		t.Fatalf("New called %d times, want 1", calls)
	}
}

func TestPoolNilConstructorPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil constructor")
		}
	}()
	_ = NewPool[int](nil)
}

func TestPoolConcurrent(t *testing.T) {
	p := NewPool(func() *bytes.Buffer {
		return new(bytes.Buffer)
	})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := p.Get()
			b.WriteString("x")
			p.Put(b)
		}()
	}
	wg.Wait()
}

func TestBufferPoolGetResets(t *testing.T) {
	p := NewBufferPool()

	// 第一次 Get 是新对象
	buf := p.Get()
	if buf.Len() != 0 {
		t.Fatalf("new buffer Len = %d, want 0", buf.Len())
	}
	buf.WriteString("hello")
	buf.WriteString(" world")
	if buf.Len() != 11 {
		t.Fatalf("buffer Len = %d, want 11", buf.Len())
	}
	p.Put(buf)

	// 再次 Get 应该已 Reset
	buf2 := p.Get()
	if buf2.Len() != 0 {
		t.Fatalf("after reset Len = %d, want 0", buf2.Len())
	}
	p.Put(buf2)
}

func TestBufferPoolPutNilIgnored(t *testing.T) {
	p := NewBufferPool()
	p.Put(nil) // 不应 panic
	got := p.Get()
	if got == nil {
		t.Fatal("Get returned nil")
	}
	p.Put(got)
}

func TestBytePoolGetReturnsEmptySlice(t *testing.T) {
	p := NewBytePool(256)
	b := p.Get()
	if len(*b) != 0 {
		t.Fatalf("len = %d, want 0", len(*b))
	}
	if cap(*b) != 256 {
		t.Fatalf("cap = %d, want 256", cap(*b))
	}
	*b = append(*b, "data"...)
	p.Put(b)

	// 再次 Get 应为 len 0
	b2 := p.Get()
	if len(*b2) != 0 {
		t.Fatalf("after Put/Get len = %d, want 0", len(*b2))
	}
	p.Put(b2)
}

func TestBytePoolPutNilIgnored(t *testing.T) {
	p := NewBytePool(64)
	p.Put(nil) // 不应 panic
}

func TestBytePoolWithMaxDropsOversized(t *testing.T) {
	const maxCap = 1024
	p := NewBytePoolWithMax(64, maxCap)

	// 创建一个超过 maxCap 的缓冲区
	big := make([]byte, 0, maxCap+1)
	bigPtr := &big
	p.Put(bigPtr)

	// Get 应该返回一个 cap <= maxCap 的缓冲区（New 创建的，cap=64）
	got := p.Get()
	if cap(*got) > maxCap {
		t.Fatalf("pooled cap = %d, want <= %d", cap(*got), maxCap)
	}
	p.Put(got)
}

func TestBytePoolWithMaxKeepsWithinLimit(t *testing.T) {
	const maxCap = 1024
	p := NewBytePoolWithMax(64, maxCap)

	// Put 确定性地清空合规缓冲区并保持容量；sync.Pool 不保证下次取回同一对象。
	b := make([]byte, 100, 128)
	p.Put(&b)
	if len(b) != 0 || cap(b) != 128 {
		t.Fatalf("after Put len/cap = %d/%d, want 0/128", len(b), cap(b))
	}

	// Get 可以返回归还的缓冲区，也可以重新创建初始容量的缓冲区。
	got := p.Get()
	if len(*got) != 0 || (cap(*got) != 64 && cap(*got) != 128) {
		t.Fatalf("pooled len/cap = %d/%d, want 0 and cap 64 or 128", len(*got), cap(*got))
	}
	p.Put(got)
}

func TestBytePoolNegativeInitCap(t *testing.T) {
	p := NewBytePool(-1)
	b := p.Get()
	if len(*b) != 0 {
		t.Fatalf("len = %d, want 0", len(*b))
	}
	p.Put(b)
}

func TestBytePoolConcurrent(t *testing.T) {
	p := NewBytePoolWithMax(128, 64*1024)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			b := p.Get()
			*b = append(*b, byte(n))
			p.Put(b)
		}(i)
	}
	wg.Wait()
}

// 构造器始终提供脏对象，因此无需依赖 sync.Pool 的随机复用即可验证 Get 的清理合同。
func TestBufferPoolGetResetsDirtyFixture(t *testing.T) {
	p := &BufferPool{pool: sync.Pool{New: func() any { return bytes.NewBufferString("dirty fixture") }}}
	for range 3 {
		buffer := p.Get()
		if buffer.Len() != 0 {
			t.Fatalf("Get did not reset dirty buffer: len=%d", buffer.Len())
		}
		buffer.WriteString("dirty again")
		p.Put(buffer)
	}
}

func TestBytePoolGetResetsDirtyFixture(t *testing.T) {
	p := &BytePool{pool: sync.Pool{New: func() any { buffer := make([]byte, 20, 128); return &buffer }}}
	for range 3 {
		buffer := p.Get()
		if len(*buffer) != 0 || cap(*buffer) != 128 {
			t.Fatalf("Get changed reset/capacity contract: len=%d cap=%d", len(*buffer), cap(*buffer))
		}
		*buffer = append(*buffer, "dirty again"...)
		p.Put(buffer)
	}
}
