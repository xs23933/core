package etcd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

func TestLeaseOperationsRejectInvalidArguments(t *testing.T) {
	discovery := &Discovery{opts: &Options{Namespace: "xpay-test"}}
	if _, err := discovery.GrantLease(context.Background(), 0); !errors.Is(err, ErrInvalidLeaseTTL) {
		t.Fatalf("GrantLease error = %v, want %v", err, ErrInvalidLeaseTTL)
	}
	if _, err := discovery.KeepAliveLease(context.Background(), 0); !errors.Is(err, ErrInvalidLeaseID) {
		t.Fatalf("KeepAliveLease error = %v, want %v", err, ErrInvalidLeaseID)
	}
	if err := discovery.RevokeLease(context.Background(), 0); !errors.Is(err, ErrInvalidLeaseID) {
		t.Fatalf("RevokeLease error = %v, want %v", err, ErrInvalidLeaseID)
	}
	if _, _, err := discovery.GetRevision(context.Background(), "/absolute"); !errors.Is(err, ErrInvalidLeaseKey) {
		t.Fatalf("GetRevision error = %v, want %v", err, ErrInvalidLeaseKey)
	}
	if _, err := discovery.CompareAndPut(context.Background(), "", 0, "value", 1); !errors.Is(err, ErrInvalidLeaseKey) {
		t.Fatalf("CompareAndPut empty key error = %v, want %v", err, ErrInvalidLeaseKey)
	}
	if _, err := discovery.CompareAndPut(context.Background(), "key", 0, "value", 0); !errors.Is(err, ErrInvalidLeaseID) {
		t.Fatalf("CompareAndPut lease error = %v, want %v", err, ErrInvalidLeaseID)
	}
}

func TestLeaseCompareAndPutLifecycle(t *testing.T) {
	discovery := startLeaseDiscovery(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	leaseID, err := discovery.GrantLease(ctx, 2)
	if err != nil {
		t.Fatalf("grant lease: %v", err)
	}
	const logicalKey = "adapter-online/bkash/instance-a"
	created, err := discovery.CompareAndPut(ctx, logicalKey, 0, "v1", leaseID)
	if err != nil || !created {
		t.Fatalf("create leased key: succeeded=%v err=%v", created, err)
	}

	physicalKey := "/xpay-test/config/" + logicalKey
	response, err := discovery.client.Get(ctx, physicalKey)
	if err != nil {
		t.Fatalf("get physical key: %v", err)
	}
	if len(response.Kvs) != 1 || string(response.Kvs[0].Key) != physicalKey {
		t.Fatalf("physical keys = %#v, want exactly %q", response.Kvs, physicalKey)
	}
	rootResponse, err := discovery.client.Get(ctx, "/", clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("scan etcd keys: %v", err)
	}
	if len(rootResponse.Kvs) != 1 {
		t.Fatalf("etcd key count = %d, want 1", len(rootResponse.Kvs))
	}

	created, err = discovery.CompareAndPut(ctx, logicalKey, 0, "duplicate", leaseID)
	if err != nil || created {
		t.Fatalf("duplicate create: succeeded=%v err=%v", created, err)
	}
	revision, found, err := discovery.GetRevision(ctx, logicalKey)
	if err != nil || !found {
		t.Fatalf("get revision: found=%v err=%v", found, err)
	}
	if revision.Value != "v1" || revision.ModRevision <= 0 {
		t.Fatalf("revision = %#v", revision)
	}

	updated, err := discovery.CompareAndPut(ctx, logicalKey, revision.ModRevision, "v2", leaseID)
	if err != nil || !updated {
		t.Fatalf("update leased key: succeeded=%v err=%v", updated, err)
	}
	updated, err = discovery.CompareAndPut(ctx, logicalKey, revision.ModRevision, "stale", leaseID)
	if err != nil || updated {
		t.Fatalf("stale update: succeeded=%v err=%v", updated, err)
	}

	keepAliveContext, stopKeepAlive := context.WithCancel(ctx)
	keepAlive, err := discovery.KeepAliveLease(keepAliveContext, leaseID)
	if err != nil {
		t.Fatalf("keep lease alive: %v", err)
	}
	select {
	case response := <-keepAlive:
		if response.ID != leaseID || response.TTL <= 0 {
			t.Fatalf("keepalive response = %#v", response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for keepalive response")
	}
	stopKeepAlive()
	assertLeaseChannelClosed(t, keepAlive)

	if err := discovery.RevokeLease(ctx, leaseID); err != nil {
		t.Fatalf("revoke lease: %v", err)
	}
	waitForLeaseKey(t, discovery, logicalKey, false, 3*time.Second)
}

func TestLeaseExpiryAndDiscoveryClose(t *testing.T) {
	discovery := startLeaseDiscovery(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	expiringLease, err := discovery.GrantLease(ctx, 1)
	if err != nil {
		t.Fatalf("grant expiring lease: %v", err)
	}
	if succeeded, err := discovery.CompareAndPut(ctx, "adapter-online/maya/instance-a", 0, "maya", expiringLease); err != nil || !succeeded {
		t.Fatalf("put expiring lease: succeeded=%v err=%v", succeeded, err)
	}
	waitForLeaseKey(t, discovery, "adapter-online/maya/instance-a", false, 6*time.Second)

	activeLease, err := discovery.GrantLease(ctx, 5)
	if err != nil {
		t.Fatalf("grant active lease: %v", err)
	}
	keepAlive, err := discovery.KeepAliveLease(ctx, activeLease)
	if err != nil {
		t.Fatalf("keep active lease alive: %v", err)
	}
	select {
	case <-keepAlive:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for active keepalive")
	}
	if err := discovery.Close(); err != nil {
		t.Fatalf("close discovery: %v", err)
	}
	assertLeaseChannelClosed(t, keepAlive)
}

func startLeaseDiscovery(t *testing.T) *Discovery {
	t.Helper()
	config := embed.NewConfig()
	config.Dir = t.TempDir()
	config.LogLevel = "error"
	clientURL := reserveEtcdURL(t)
	peerURL := reserveEtcdURL(t)
	config.ListenClientUrls = []url.URL{clientURL}
	config.AdvertiseClientUrls = []url.URL{clientURL}
	config.ListenPeerUrls = []url.URL{peerURL}
	config.AdvertisePeerUrls = []url.URL{peerURL}
	config.InitialCluster = fmt.Sprintf("%s=%s", config.Name, peerURL.String())

	server, err := embed.StartEtcd(config)
	if err != nil {
		t.Fatalf("start embedded etcd: %v", err)
	}
	t.Cleanup(server.Close)
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		server.Server.Stop()
		t.Fatal("embedded etcd did not become ready")
	}

	discovery, err := NewDiscovery(&Options{
		Namespace:   "xpay-test",
		Endpoints:   []string{"http://" + server.Clients[0].Addr().String()},
		DialTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("create discovery: %v", err)
	}
	t.Cleanup(func() { _ = discovery.Close() })
	return discovery
}

func reserveEtcdURL(t *testing.T) url.URL {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve etcd address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved etcd address: %v", err)
	}
	return url.URL{Scheme: "http", Host: address}
}

func assertLeaseChannelClosed(t *testing.T, channel <-chan LeaseKeepAlive) {
	t.Helper()
	select {
	case _, open := <-channel:
		if open {
			t.Fatal("keepalive channel remained open")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for keepalive channel to close")
	}
}

func waitForLeaseKey(t *testing.T, discovery *Discovery, key string, wantFound bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, found, err := discovery.GetRevision(context.Background(), key)
		if err != nil {
			t.Fatalf("get leased key %q: %v", key, err)
		}
		if found == wantFound {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("leased key %q found=%v, want %v", key, found, wantFound)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
