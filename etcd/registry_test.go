package etcd

import (
	"context"
	"testing"
	"time"
)

func TestRegistryDeregisterPreservesSameKeyReplacement(t *testing.T) {
	discovery := startLeaseDiscovery(t)
	base := Options{
		Namespace:   "xpay-test",
		Endpoints:   append([]string(nil), discovery.opts.Endpoints...),
		DialTimeout: 3 * time.Second,
		ServiceName: "payments",
		ServiceAddr: "127.0.0.1:39003",
		ServiceID:   "payments-a",
		TTL:         10,
	}
	oldRegistry, err := NewRegistry(&base)
	if err != nil {
		t.Fatalf("create old Registry: %v", err)
	}
	if err := oldRegistry.Register(); err != nil {
		t.Fatalf("register old Registry: %v", err)
	}

	replacementOptions := base
	replacementOptions.Version = "replacement"
	replacement, err := NewRegistry(&replacementOptions)
	if err != nil {
		t.Fatalf("create replacement Registry: %v", err)
	}
	if err := replacement.Register(); err != nil {
		t.Fatalf("register replacement Registry: %v", err)
	}
	t.Cleanup(func() { _ = replacement.Deregister() })

	if err := oldRegistry.Deregister(); err != nil {
		t.Fatalf("deregister old Registry: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := discovery.client.Get(ctx, replacementOptions.ServiceKey())
	if err != nil {
		t.Fatalf("get replacement key: %v", err)
	}
	if len(response.Kvs) != 1 {
		t.Fatalf("replacement key count = %d, want 1 after old Registry retirement", len(response.Kvs))
	}
	if got := response.Kvs[0].Lease; got != int64(replacement.leaseID) {
		t.Fatalf("replacement key lease = %d, want %d", got, replacement.leaseID)
	}
}
