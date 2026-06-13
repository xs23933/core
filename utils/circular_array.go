package utils

import "sync"

type CircularBuffer[T comparable] struct {
	data     []T
	head     int
	size     int
	capacity int
	mu       sync.RWMutex
	// 使用 map 来快速查找，牺牲空间换时间
	indexMap map[T]int
}

func NewCircularBuffer[T comparable](capacity int) *CircularBuffer[T] {
	return &CircularBuffer[T]{
		data:     make([]T, capacity),
		capacity: capacity,
		indexMap: make(map[T]int),
	}
}

func (cb *CircularBuffer[T]) Save(value T) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// 快速检查是否存在
	if _, exists := cb.indexMap[value]; exists {
		return false
	}

	// 如果缓冲区已满，删除最旧的项目
	if cb.size == cb.capacity {
		oldest := cb.data[cb.head]
		delete(cb.indexMap, oldest)
	} else {
		cb.size++
	}

	// 保存新签名
	cb.data[cb.head] = value
	cb.indexMap[value] = cb.head
	cb.head = (cb.head + 1) % cb.capacity
	return true
}

func (cb *CircularBuffer[T]) Contains(value T) bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	_, exists := cb.indexMap[value]
	return exists
}

func (cb *CircularBuffer[T]) Delete(value T) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if idx, ok := cb.indexMap[value]; ok {
		delete(cb.indexMap, value)
		var zero T
		cb.data[idx] = zero // 清理内容，避免内存泄漏
		cb.size--
	}
}

// Values 返回当前缓冲区中的所有值（按插入顺序）
func (cb *CircularBuffer[T]) Values() []T {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	res := make([]T, 0, cb.size)
	start := (cb.head - cb.size + cb.capacity) % cb.capacity
	for i := 0; i < cb.size; i++ {
		idx := (start + i) % cb.capacity
		res = append(res, cb.data[idx])
	}
	return res
}
