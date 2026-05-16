package etcd

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type Discovery struct {
	client *clientv3.Client
	opts   *Options

	mu sync.RWMutex

	// serviceName -> instanceKey -> service
	services map[string]map[string]*ServiceInfo

	// serviceName -> cancel
	watchers map[string]context.CancelFunc

	// serviceName -> subscriberID -> callback
	subscribers map[string]map[uint64]func()

	subID atomic.Uint64
}

func NewDiscovery(opts *Options) (*Discovery, error) {
	if opts == nil {
		opts = DefaultOptions()
	}

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   opts.Endpoints,
		Username:    opts.Username,
		Password:    opts.Password,
		DialTimeout: opts.DialTimeout,
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err = client.Get(ctx, "health"); err != nil {
		client.Close()
		return nil, fmt.Errorf("etcd connection test failed: %w", err)
	}

	return &Discovery{
		client:      client,
		opts:        opts,
		services:    make(map[string]map[string]*ServiceInfo),
		watchers:    make(map[string]context.CancelFunc),
		subscribers: make(map[string]map[uint64]func()),
	}, nil
}

func (d *Discovery) Watch(serviceName string) error {
	d.mu.Lock()
	if _, ok := d.watchers[serviceName]; ok {
		d.mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.watchers[serviceName] = cancel

	if d.services[serviceName] == nil {
		d.services[serviceName] = make(map[string]*ServiceInfo)
	}
	d.mu.Unlock()

	prefix := fmt.Sprintf("/services/%s/", serviceName)

	getCtx, getCancel := context.WithTimeout(ctx, 10*time.Second)
	defer getCancel()

	resp, err := d.client.Get(getCtx, prefix, clientv3.WithPrefix())
	if err != nil {
		cancel()
		d.mu.Lock()
		delete(d.watchers, serviceName)
		d.mu.Unlock()
		return err
	}

	d.mu.Lock()
	for _, kv := range resp.Kvs {
		var svc ServiceInfo
		if err := json.Unmarshal(kv.Value, &svc); err != nil {
			continue
		}
		d.services[serviceName][string(kv.Key)] = &svc
	}
	d.mu.Unlock()

	d.notify(serviceName)

	go d.watchLoop(ctx, serviceName, prefix)

	return nil
}

func (d *Discovery) watchLoop(ctx context.Context, serviceName string, prefix string) {
	defer func() {
		d.mu.Lock()
		delete(d.watchers, serviceName)
		d.mu.Unlock()
	}()

	watchCh := d.client.Watch(ctx, prefix, clientv3.WithPrefix())

	for resp := range watchCh {
		if err := resp.Err(); err != nil {
			continue
		}
		changed := false

		d.mu.Lock()
		for _, ev := range resp.Events {
			key := string(ev.Kv.Key)

			switch ev.Type {
			case clientv3.EventTypePut:
				var svc ServiceInfo
				if err := json.Unmarshal(ev.Kv.Value, &svc); err != nil {
					continue
				}
				if d.services[serviceName] == nil {
					d.services[serviceName] = make(map[string]*ServiceInfo)
				}
				d.services[serviceName][key] = &svc
				changed = true

			case clientv3.EventTypeDelete:
				if d.services[serviceName] != nil {
					delete(d.services[serviceName], key)
				}
				changed = true
			}
		}
		d.mu.Unlock()

		if changed {
			d.notify(serviceName)
		}
	}
}

func (d *Discovery) GetServices(serviceName string) []*ServiceInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()

	m := d.services[serviceName]
	services := make([]*ServiceInfo, 0, len(m))
	for _, svc := range m {
		services = append(services, svc)
	}
	return services
}

func (d *Discovery) Subscribe(serviceName string, fn func()) func() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.subscribers[serviceName] == nil {
		d.subscribers[serviceName] = make(map[uint64]func())
	}

	id := d.subID.Add(1)
	d.subscribers[serviceName][id] = fn

	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if subs, ok := d.subscribers[serviceName]; ok {
			delete(subs, id)
		}
	}
}

func (d *Discovery) notify(serviceName string) {
	d.mu.RLock()
	subs := make([]func(), 0, len(d.subscribers[serviceName]))
	for _, fn := range d.subscribers[serviceName] {
		subs = append(subs, fn)
	}
	d.mu.RUnlock()

	for _, fn := range subs {
		fn()
	}
}

func (d *Discovery) Close() error {
	d.mu.Lock()
	for _, cancel := range d.watchers {
		cancel()
	}
	d.watchers = make(map[string]context.CancelFunc)
	d.mu.Unlock()

	return d.client.Close()
}
