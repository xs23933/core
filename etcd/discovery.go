package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

var errWatchClosed = errors.New("etcd watch closed")

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

func parseServiceKey(key, serviceRoot string) (serviceName, instanceID string, ok bool) {
	if !strings.HasPrefix(key, serviceRoot) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(key, serviceRoot), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
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

	serviceOpts := *d.opts
	serviceOpts.ServiceName = serviceName
	prefix := serviceOpts.ServicePrefix()

	getCtx, getCancel := context.WithTimeout(ctx, 10*time.Second)
	defer getCancel()

	revision, err := d.refreshService(getCtx, serviceName, prefix)
	if err != nil {
		cancel()
		d.mu.Lock()
		delete(d.watchers, serviceName)
		d.mu.Unlock()
		return err
	}

	go d.watchLoop(ctx, serviceName, prefix, revision+1)

	return nil
}

func (d *Discovery) refreshService(ctx context.Context, serviceName string, prefix string) (int64, error) {
	resp, err := d.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return 0, err
	}

	snapshot := make(map[string]*ServiceInfo, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var svc ServiceInfo
		if err := json.Unmarshal(kv.Value, &svc); err != nil {
			continue
		}
		_, instanceID, ok := parseServiceKey(string(kv.Key), d.opts.ServiceRoot())
		if ok && svc.ID == "" {
			svc.ID = instanceID
		}
		snapshot[string(kv.Key)] = &svc
	}

	if d.replaceServiceSnapshot(serviceName, snapshot) {
		d.notify(serviceName)
	}
	return resp.Header.GetRevision(), nil
}

func (d *Discovery) replaceServiceSnapshot(serviceName string, snapshot map[string]*ServiceInfo) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	services := copyServices(d.loadServices())
	if services == nil {
		services = make(map[string]map[string]*ServiceInfo)
	}
	if services[serviceName] == nil {
		services[serviceName] = make(map[string]*ServiceInfo)
	}
	if reflect.DeepEqual(services[serviceName], snapshot) {
		return false
	}

	services[serviceName] = snapshot
	d.storeServices(services)
	return true
}

func (d *Discovery) watchLoop(ctx context.Context, serviceName string, prefix string, startRev int64) {
	defer func() {
		d.mu.Lock()
		delete(d.watchers, serviceName)
		d.mu.Unlock()
	}()

	nextRev := startRev
	_ = runRetryLoop(ctx, time.Second, func(loopCtx context.Context) error {
		revision, err := d.watchService(loopCtx, serviceName, prefix, nextRev)
		if revision > 0 {
			nextRev = revision
		}
		if err == nil {
			return nil
		}
		if loopCtx.Err() != nil {
			return loopCtx.Err()
		}

		refreshCtx, cancel := context.WithTimeout(loopCtx, 10*time.Second)
		defer cancel()
		refreshedRevision, refreshErr := d.refreshService(refreshCtx, serviceName, prefix)
		if refreshErr != nil {
			return refreshErr
		}
		nextRev = refreshedRevision + 1
		return err
	})
}

func (d *Discovery) watchService(ctx context.Context, serviceName string, prefix string, startRev int64) (int64, error) {
	opts := []clientv3.OpOption{clientv3.WithPrefix()}
	if startRev > 0 {
		opts = append(opts, clientv3.WithRev(startRev))
	}
	watchCh := d.client.Watch(ctx, prefix, opts...)
	nextRev := startRev

	for resp := range watchCh {
		if resp.Header.GetRevision() > 0 {
			nextRev = resp.Header.GetRevision() + 1
		}
		if err := resp.Err(); err != nil {
			return nextRev, err
		}
		if d.applyWatchEvents(serviceName, resp.Events) {
			d.notify(serviceName)
		}
	}
	if err := ctx.Err(); err != nil {
		return nextRev, err
	}
	return nextRev, errWatchClosed
}

func (d *Discovery) applyWatchEvents(serviceName string, events []*clientv3.Event) bool {
	changed := false

	d.mu.Lock()
	services := copyServices(d.loadServices())
	if services == nil {
		services = make(map[string]map[string]*ServiceInfo)
	}
	for _, ev := range events {
		key := string(ev.Kv.Key)

		switch ev.Type {
		case clientv3.EventTypePut:
			var svc ServiceInfo
			if err := json.Unmarshal(ev.Kv.Value, &svc); err != nil {
				continue
			}
			_, instanceID, ok := parseServiceKey(key, d.opts.ServiceRoot())
			if ok && svc.ID == "" {
				svc.ID = instanceID
			}
			if services[serviceName] == nil {
				services[serviceName] = make(map[string]*ServiceInfo)
			}
			if !reflect.DeepEqual(services[serviceName][key], &svc) {
				services[serviceName][key] = &svc
				changed = true
			}

		case clientv3.EventTypeDelete:
			if services[serviceName] != nil {
				if _, ok := services[serviceName][key]; ok {
					delete(services[serviceName], key)
					changed = true
				}
			}
		}
	}
	if changed {
		d.storeServices(services)
	}
	d.mu.Unlock()

	return changed
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
