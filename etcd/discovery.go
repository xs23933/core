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

	mu sync.Mutex

	// serviceName -> instanceKey -> service
	services atomic.Value // map[string]map[string]*ServiceInfo, copy-on-write

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

	d := &Discovery{
		client:      client,
		opts:        opts,
		watchers:    make(map[string]context.CancelFunc),
		subscribers: make(map[string]map[uint64]func()),
	}
	d.storeServices(make(map[string]map[string]*ServiceInfo))
	return d, nil
}

func (d *Discovery) loadServices() map[string]map[string]*ServiceInfo {
	if services, ok := d.services.Load().(map[string]map[string]*ServiceInfo); ok && services != nil {
		return services
	}
	return nil
}

func (d *Discovery) storeServices(services map[string]map[string]*ServiceInfo) {
	d.services.Store(services)
}

func copyServices(services map[string]map[string]*ServiceInfo) map[string]map[string]*ServiceInfo {
	next := make(map[string]map[string]*ServiceInfo, len(services))
	for serviceName, instances := range services {
		copied := make(map[string]*ServiceInfo, len(instances))
		for key, svc := range instances {
			copied[key] = svc
		}
		next[serviceName] = copied
	}
	return next
}

func (d *Discovery) Watch(serviceName string) error {
	d.mu.Lock()
	if _, ok := d.watchers[serviceName]; ok {
		d.mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.watchers[serviceName] = cancel

	services := copyServices(d.loadServices())
	if services[serviceName] == nil {
		services[serviceName] = make(map[string]*ServiceInfo)
	}
	d.storeServices(services)
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
	services = copyServices(d.loadServices())
	if services[serviceName] == nil {
		services[serviceName] = make(map[string]*ServiceInfo)
	}
	for _, kv := range resp.Kvs {
		var svc ServiceInfo
		if err := json.Unmarshal(kv.Value, &svc); err != nil {
			continue
		}
		services[serviceName][string(kv.Key)] = &svc
	}
	d.storeServices(services)
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
		services := copyServices(d.loadServices())
		for _, ev := range resp.Events {
			key := string(ev.Kv.Key)

			switch ev.Type {
			case clientv3.EventTypePut:
				var svc ServiceInfo
				if err := json.Unmarshal(ev.Kv.Value, &svc); err != nil {
					continue
				}
				if services[serviceName] == nil {
					services[serviceName] = make(map[string]*ServiceInfo)
				}
				services[serviceName][key] = &svc
				changed = true

			case clientv3.EventTypeDelete:
				if services[serviceName] != nil {
					delete(services[serviceName], key)
				}
				changed = true
			}
		}
		if changed {
			d.storeServices(services)
		}
		d.mu.Unlock()

		if changed {
			d.notify(serviceName)
		}
	}
}

func (d *Discovery) GetServices(serviceName string) []*ServiceInfo {
	m := d.loadServices()[serviceName]
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
	d.mu.Lock()
	subs := make([]func(), 0, len(d.subscribers[serviceName]))
	for _, fn := range d.subscribers[serviceName] {
		subs = append(subs, fn)
	}
	d.mu.Unlock()

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
