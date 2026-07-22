package gateway

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"testing"
	"time"

	coreetcd "github.com/xs23933/core/v3/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

func TestClientRouteStoreLifecycle(t *testing.T) {
	client, _ := startGatewayRouteStoreEtcd(t)
	store := clientRouteStore{client: client}
	assertRouteStorePut(t, store, client)
}

func TestDiscoveryRouteStoreStoresJSONBytesWithoutBase64Encoding(t *testing.T) {
	client, endpoint := startGatewayRouteStoreEtcd(t)
	discovery, err := coreetcd.NewDiscovery(&coreetcd.Options{Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("create discovery: %v", err)
	}
	t.Cleanup(func() { _ = discovery.Close() })

	store := discoveryRouteStore{discovery: discovery}
	assertRouteStorePut(t, store, client)
}

type routeStoreContract interface {
	Put(context.Context, string, []byte) error
}

func assertRouteStorePut(t *testing.T, store routeStoreContract, client *clientv3.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const key = "/gateway/routes/manual/slot"
	const value = `{"id":"manual"}`
	if err := store.Put(ctx, key, []byte(value)); err != nil {
		t.Fatalf("put route: %v", err)
	}
	response, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if len(response.Kvs) != 1 || string(response.Kvs[0].Value) != value {
		t.Fatalf("stored route = %#v, want raw JSON %s", response.Kvs, value)
	}
}

func startGatewayRouteStoreEtcd(t *testing.T) (*clientv3.Client, string) {
	t.Helper()
	config := embed.NewConfig()
	config.Dir = t.TempDir()
	config.LogLevel = "error"
	clientURL := reserveGatewayRouteStoreURL(t)
	peerURL := reserveGatewayRouteStoreURL(t)
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

	endpoint := "http://" + server.Clients[0].Addr().String()
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("create etcd client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, endpoint
}

func reserveGatewayRouteStoreURL(t *testing.T) url.URL {
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
