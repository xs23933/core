package utils

import "sync"

type CircularMapBuffer[K comparable, T any] struct {
	capacity int
	data     map[K]T
	order    []K
	start    int
	size     int
	mu       sync.RWMutex
}

func NewStringCircularMapBuffer[T any](capacity int) *CircularMapBuffer[string, T] {
	return &CircularMapBuffer[string, T]{
		capacity: capacity,
		data:     make(map[string]T, capacity),
		order:    make([]string, capacity),
	}
}

func NewInt64CircularMapBuffer[T any](capacity int) *CircularMapBuffer[int64, T] {
	return &CircularMapBuffer[int64, T]{
		capacity: capacity,
		data:     make(map[int64]T, capacity),
		order:    make([]int64, capacity),
	}
}

func (cb *CircularMapBuffer[K, T]) Save(id K, value T) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// 如果已存在，更新
	if _, exists := cb.data[id]; exists {
		cb.data[id] = value
		return
	}

	// 缓冲区已满 -> 移除最旧的
	if cb.size == cb.capacity {
		oldestID := cb.order[cb.start]
		delete(cb.data, oldestID)
		cb.start = (cb.start + 1) % cb.capacity
	} else {
		cb.size++
	}

	// 插入新元素
	insertIdx := (cb.start + cb.size - 1) % cb.capacity
	cb.data[id] = value
	cb.order[insertIdx] = id
}

func (cb *CircularMapBuffer[K, T]) Get(id K) (T, bool) {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	secret, exists := cb.data[id]
	return secret, exists
}

func (cb *CircularMapBuffer[K, T]) Delete(id K) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if _, exists := cb.data[id]; !exists {
		return
	}
	delete(cb.data, id)
	var zero K
	for i := 0; i < cb.size; i++ {
		idx := (cb.start + i) % cb.capacity
		if cb.order[idx] == id {
			// 将后续元素向前移动
			for j := i; j < cb.size-1; j++ {
				currentIdx := (cb.start + j) % cb.capacity
				nextIdx := (cb.start + j + 1) % cb.capacity
				cb.order[currentIdx] = cb.order[nextIdx]
			}
			// 清空最后一个元素的位置
			lastIdx := (cb.start + cb.size - 1) % cb.capacity
			cb.order[lastIdx] = zero
			cb.size--
			return
		}
	}
}

// Values 返回当前缓冲区中的所有值（按插入顺序）
func (cb *CircularMapBuffer[K, T]) Values() []T {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	res := make([]T, 0, cb.size)
	start := (cb.start - cb.size + cb.capacity) % cb.capacity
	for i := 0; i < cb.size; i++ {
		idx := (start + i) % cb.capacity
		res = append(res, cb.data[cb.order[idx]])
	}
	return res
}

// 添加回调函数处理查找数据
func (cb *CircularMapBuffer[K, T]) Find(fn func(key K, value T) bool) (K, T, bool) {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	var (
		zero  T
		zeroK K
	)
	for i := 0; i < cb.size; i++ {
		idx := (cb.start + i) % cb.capacity
		key := cb.order[idx]
		value, ok := cb.data[key]
		if !ok {
			continue
		}
		if fn(key, value) {
			return key, value, true
		}
	}
	return zeroK, zero, false
}
