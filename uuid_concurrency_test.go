package core

import (
	"sync"
	"testing"
)

func TestNewUUIDConcurrent(t *testing.T) {
	const workers, perWorker = 32, 256
	values := make(chan UUID, workers*perWorker)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				values <- NewUUID()
			}
		})
	}
	wg.Wait()
	close(values)
	seen := make(map[UUID]struct{}, workers*perWorker)
	for value := range values {
		if value.IsEmpty() || value.Version() != 4 {
			t.Fatalf("无效 UUID: %v", value)
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("重复 UUID: %v", value)
		}
		seen[value] = struct{}{}
		parsed, err := UUIDFromString(value.String())
		if err != nil || parsed != value {
			t.Fatalf("UUID 格式不能往返: %v", value)
		}
	}
}
