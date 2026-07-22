package gateway

import (
	"context"
	"errors"

	"github.com/xs23933/core/v3/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type discoveryRouteStore struct {
	discovery *etcd.Discovery
}

func (s discoveryRouteStore) Put(ctx context.Context, key string, value []byte) error {
	if s.discovery == nil {
		return errors.New("gateway: etcd discovery is not enabled")
	}
	return s.discovery.PutStringContext(ctx, key, string(value))
}

// clientRouteStore uses the Gateway-owned etcd client and does not depend on
// app.EtcdDiscovery being enabled.
type clientRouteStore struct {
	client *clientv3.Client
}

func (s clientRouteStore) Put(ctx context.Context, key string, value []byte) error {
	if s.client == nil {
		return errors.New("gateway: etcd client is nil")
	}
	_, err := s.client.Put(ctx, key, string(value))
	return err
}
