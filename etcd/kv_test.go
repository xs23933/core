package etcd

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestKVKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "relative config key", key: "gateway/public_routes", want: "/config/gateway/public_routes"},
		{name: "absolute route key", key: "/gateway/routes/route-id", want: "/gateway/routes/route-id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kvKey(tt.key); got != tt.want {
				t.Fatalf("kvKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestDiscoveryRefreshServiceReplacesStaleInstances(t *testing.T) {
	d := &Discovery{}
	d.storeServices(map[string]map[string]*ServiceInfo{
		"billing": {
			"/services/billing/old": {Name: "billing", ID: "old", Addr: "127.0.0.1:17001"},
		},
	})

	changed := d.replaceServiceSnapshot("billing", map[string]*ServiceInfo{
		"/services/billing/new": {Name: "billing", ID: "new", Addr: "127.0.0.1:17002"},
	})
	if !changed {
		t.Fatal("replaceServiceSnapshot should report changed when stale instances are removed")
	}

	services := d.GetServices("billing")
	if len(services) != 1 {
		t.Fatalf("service count = %d, want 1", len(services))
	}
	if services[0].ID != "new" || services[0].Addr != "127.0.0.1:17002" {
		t.Fatalf("service = %+v, want new instance", services[0])
	}
}

func TestRunRetryLoopRetriesAfterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	attempts := make(chan int, 3)
	count := 0
	err := runRetryLoop(ctx, time.Millisecond, func(context.Context) error {
		count++
		attempts <- count
		if count == 1 {
			return errors.New("temporary failure")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("runRetryLoop error = %v, want nil", err)
	}
	if got := <-attempts; got != 1 {
		t.Fatalf("first attempt = %d, want 1", got)
	}
	if got := <-attempts; got != 2 {
		t.Fatalf("second attempt = %d, want 2", got)
	}
}
