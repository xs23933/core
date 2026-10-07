package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func registryOptions(t *testing.T, discovery *Discovery, id string) Options {
	t.Helper()
	return Options{Namespace: "xpay-test", Endpoints: discovery.opts.Endpoints, DialTimeout: 3 * time.Second, ServiceName: "recovery", ServiceID: id, ServiceAddr: "127.0.0.1:39001", TTL: 2}
}

func readRegistry(t *testing.T, d *Discovery, options Options) (*ServiceInfo, int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := d.client.Get(ctx, options.ServiceKey())
	if err != nil || len(response.Kvs) != 1 {
		t.Fatalf("registration: response=%v err=%v", response, err)
	}
	var info ServiceInfo
	if err := json.Unmarshal(response.Kvs[0].Value, &info); err != nil {
		t.Fatal(err)
	}
	return &info, response.Kvs[0].Lease
}

func TestRegistryRecoveryCannotOverwriteSuccessor(t *testing.T) {
	d := startLeaseDiscovery(t)
	options := registryOptions(t, d, "fixed")
	old, err := NewRegistry(&options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Deregister() })
	if err := old.doRegister(); err != nil {
		t.Fatal(err)
	}
	old.clearLease()
	options.Version = "successor"
	next, err := NewRegistry(&options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Deregister() })
	if err := next.Register(); err != nil {
		t.Fatal(err)
	}
	if err := old.doRegister(); !errors.Is(err, ErrRegistrationSuperseded) {
		t.Fatalf("old recovery=%v", err)
	}
	if err := old.SetReady(context.Background(), true); err == nil {
		t.Fatal("retired readiness unexpectedly succeeded")
	}
	if err := old.Deregister(); err != nil {
		t.Fatal(err)
	}
	info, _ := readRegistry(t, d, options)
	if info.Version != "successor" || info.Generation != next.generation {
		t.Fatalf("successor replaced: %+v", info)
	}
}

func TestRegistryReadinessSurvivesLeaseRecovery(t *testing.T) {
	d := startLeaseDiscovery(t)
	options := registryOptions(t, d, "ready")
	ready := false
	options.RegistrationReady = &ready
	r, err := NewRegistry(&options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Deregister() })
	if err := r.Register(); err != nil {
		t.Fatal(err)
	}
	info, first := readRegistry(t, d, options)
	if info.Ready == nil || *info.Ready || info.Generation == "" {
		t.Fatalf("initial registration: %+v", info)
	}
	if err := r.SetReady(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	r.mu.Lock()
	lease := r.leaseID
	r.mu.Unlock()
	if _, err := d.client.Revoke(ctx, lease); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		response, err := d.client.Get(ctx, options.ServiceKey())
		if err == nil && len(response.Kvs) == 1 && response.Kvs[0].Lease != first {
			var recovered ServiceInfo
			if err := json.Unmarshal(response.Kvs[0].Value, &recovered); err != nil {
				t.Fatal(err)
			}
			if recovered.Ready == nil || !*recovered.Ready || recovered.Generation != info.Generation {
				t.Fatalf("recovered: %+v", recovered)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("lost lease did not recover")
}

func TestRegistryAbruptExitExpiresDiscovery(t *testing.T) {
	d := startLeaseDiscovery(t)
	options := registryOptions(t, d, "expires")
	r, err := NewRegistry(&options)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(); err != nil {
		t.Fatal(err)
	}
	if err := d.Watch(options.ServiceName); err != nil {
		t.Fatal(err)
	}
	r.cancel()
	r.wg.Wait()
	_ = r.client.Close()
	stoppedAt := time.Now()
	deadline := stoppedAt.Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(d.GetServices(options.ServiceName)) == 0 {
			t.Logf("TTL discovery removal after keepalive stopped: %s", time.Since(stoppedAt))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stopped keepalive remained discovered after TTL")
}

func TestRegistryDeregisterStopsRecovery(t *testing.T) {
	d := startLeaseDiscovery(t)
	options := registryOptions(t, d, "closing")
	r, err := NewRegistry(&options)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r.mu.Lock()
	lease := r.leaseID
	r.mu.Unlock()
	if _, err := d.client.Revoke(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err := r.Deregister(); err != nil {
		t.Fatal(err)
	}
	if err := r.doRegister(); !errors.Is(err, context.Canceled) {
		t.Fatalf("late recovery=%v", err)
	}
	response, err := d.client.Get(ctx, options.ServiceKey())
	if err != nil || len(response.Kvs) != 0 {
		t.Fatalf("shutdown left registration: %+v %v", response, err)
	}
}

func TestRegistryReadinessCannotMutateSuccessor(t *testing.T) {
	d := startLeaseDiscovery(t)
	options := registryOptions(t, d, "ready-cas")
	old, err := NewRegistry(&options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Deregister() })
	if err := old.Register(); err != nil {
		t.Fatal(err)
	}
	next, err := NewRegistry(&options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Deregister() })
	if err := next.Register(); err != nil {
		t.Fatal(err)
	}
	if old.generation == next.generation {
		t.Fatal("process generations reused")
	}
	if err := old.SetReady(context.Background(), false); !errors.Is(err, ErrRegistrationSuperseded) {
		t.Fatalf("old readiness=%v", err)
	}
	info, _ := readRegistry(t, d, options)
	if info.Generation != next.generation || info.Ready != nil {
		t.Fatalf("successor mutated: %+v", info)
	}
}
