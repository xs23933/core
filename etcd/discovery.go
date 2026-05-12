package etcd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type Discovery struct {
	client    *clientv3.Client
	opts      *Options
	services  map[string]*ServiceInfo
	mu        sync.RWMutex
	watchers  map[string]context.CancelFunc
	callbacks []func(services []*ServiceInfo)
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
		return nil, fmt.Errorf("create etcd client: %w", err)
	}

	return &Discovery{
		client:   client,
		opts:     opts,
		services: make(map[string]*ServiceInfo),
		watchers: make(map[string]context.CancelFunc),
	}, nil
}

// Watch 监听服务变化
func (d *Discovery) Watch(serviceName string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 如果已经在监听，先取消
	if cancel, ok := d.watchers[serviceName]; ok {
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // 添加超时
	d.watchers[serviceName] = cancel

	prefix := fmt.Sprintf("/services/%s/", serviceName)

	// 先获取现有服务
	resp, err := d.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		cancel()
		return fmt.Errorf("get services: %w", err)
	}

	// 清空旧数据
	for key := range d.services {
		if strings.HasPrefix(key, prefix) {
			delete(d.services, key)
		}
	}

	// 添加现有服务
	for _, kv := range resp.Kvs {
		var info ServiceInfo
		if err := json.Unmarshal(kv.Value, &info); err != nil {
			fmt.Printf("Unmarshal service info: %v\n", err)
			continue
		}
		d.services[string(kv.Key)] = &info
	}

	fmt.Printf("Found %d services for %s\n", len(resp.Kvs), serviceName)

	// 通知变化
	d.notify()

	// 创建新的 context 用于监听
	watchCtx, watchCancel := context.WithCancel(context.Background())

	// 监听变化（非阻塞）
	go func() {
		defer watchCancel()
		watchCh := d.client.Watch(watchCtx, prefix, clientv3.WithPrefix())
		for watchResp := range watchCh {
			for _, ev := range watchResp.Events {
				switch ev.Type {
				case clientv3.EventTypePut:
					var info ServiceInfo
					if err := json.Unmarshal(ev.Kv.Value, &info); err != nil {
						fmt.Printf("Unmarshal service info: %v\n", err)
						continue
					}
					d.mu.Lock()
					d.services[string(ev.Kv.Key)] = &info
					d.mu.Unlock()
					fmt.Printf("Service added/updated: %s -> %s\n", ev.Kv.Key, info.Addr)

				case clientv3.EventTypeDelete:
					d.mu.Lock()
					delete(d.services, string(ev.Kv.Key))
					d.mu.Unlock()
					fmt.Printf("Service removed: %s\n", ev.Kv.Key)
				}
			}
			d.notify()
		}
	}()

	// 更新 watcher 的取消函数
	d.watchers[serviceName] = watchCancel

	return nil
}

// GetServices 获取所有服务实例
func (d *Discovery) GetServices(serviceName string) []*ServiceInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var services []*ServiceInfo
	prefix := fmt.Sprintf("/services/%s/", serviceName)

	for key, info := range d.services {
		if strings.HasPrefix(key, prefix) {
			services = append(services, info)
		}
	}

	return services
}

// OnChange 注册服务变化回调
func (d *Discovery) OnChange(callback func(services []*ServiceInfo)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.callbacks = append(d.callbacks, callback)
}

func (d *Discovery) notify() {
	d.mu.RLock()
	services := make([]*ServiceInfo, 0, len(d.services))
	for _, info := range d.services {
		services = append(services, info)
	}
	callbacks := d.callbacks
	d.mu.RUnlock()

	for _, callback := range callbacks {
		callback(services)
	}
}

// Close 关闭发现
func (d *Discovery) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, cancel := range d.watchers {
		cancel()
	}

	return d.client.Close()
}

// GetServicesFromEtcd 直接从 etcd 获取服务列表（不依赖缓存）
func (d *Discovery) GetServicesFromEtcd(serviceName string) ([]*ServiceInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	prefix := fmt.Sprintf("/services/%s/", serviceName)
	resp, err := d.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("get services from etcd: %w", err)
	}

	var services []*ServiceInfo
	for _, kv := range resp.Kvs {
		var info ServiceInfo
		if err := json.Unmarshal(kv.Value, &info); err != nil {
			continue
		}
		services = append(services, &info)
	}

	// 更新缓存
	d.mu.Lock()
	for _, svc := range services {
		key := fmt.Sprintf("/services/%s/%s", svc.Name, svc.ID)
		if svc.ID == "" {
			key = fmt.Sprintf("/services/%s/%s", svc.Name, svc.Addr)
		}
		d.services[key] = svc
	}
	d.mu.Unlock()

	return services, nil
}
