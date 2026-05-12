package etcd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type Registry struct {
	client      *clientv3.Client
	lease       clientv3.Lease
	leaseID     clientv3.LeaseID
	opts        *Options
	ctx         context.Context
	cancel      context.CancelFunc
	keepAliveCh <-chan *clientv3.LeaseKeepAliveResponse
}

// ServiceInfo 服务信息
type ServiceInfo struct {
	Name     string            `json:"name"`
	Addr     string            `json:"addr"`
	ID       string            `json:"id"`
	Version  string            `json:"version"`
	Metadata map[string]string `json:"metadata"`
	TTL      int64             `json:"ttl"`
}

func NewRegistry(opts *Options) (*Registry, error) {
	if opts == nil {
		opts = DefaultOptions()
	}

	// 创建 etcd 客户端
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   opts.Endpoints,
		Username:    opts.Username,
		Password:    opts.Password,
		DialTimeout: opts.DialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create etcd client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Registry{
		client: client,
		lease:  clientv3.NewLease(client),
		opts:   opts,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Register 注册服务
func (r *Registry) doRegister() error {
	serviceInfo := ServiceInfo{
		Name:     r.opts.ServiceName,
		Addr:     r.opts.ServiceAddr,
		ID:       r.opts.ServiceID,
		Version:  r.opts.Version,
		Metadata: r.opts.Metadata,
		TTL:      r.opts.TTL,
	}
	// 序列化服务信息
	value, err := json.Marshal(serviceInfo)
	if err != nil {
		return fmt.Errorf("marshal service info: %w", err)
	}

	// 创建租约
	grantResp, err := r.lease.Grant(r.ctx, r.opts.TTL)
	if err != nil {
		return fmt.Errorf("grant lease: %w", err)
	}
	r.leaseID = grantResp.ID

	// 注册服务
	_, err = r.client.Put(r.ctx, r.opts.ServiceKey(), string(value), clientv3.WithLease(r.leaseID))
	if err != nil {
		return fmt.Errorf("put service: %w", err)
	}

	// 自动续约
	r.keepAliveCh, err = r.lease.KeepAlive(r.ctx, r.leaseID)
	if err != nil {
		return fmt.Errorf("keep alive: %w", err)
	}

	go r.keepAlive()

	return nil
}

func (r *Registry) Register() error {
	var err error
	for i := range 3 {
		if err = r.doRegister(); err == nil {
			return nil
		}

		time.Sleep(time.Duration(i+1) * time.Second) // 指数退避
	}
	return fmt.Errorf("register failed after 3 attempts: %v", err)
}

// keepAlive 监听续约
func (r *Registry) keepAlive() {
	for {
		select {
		case _, ok := <-r.keepAliveCh:
			if !ok {
				return
			}
		case <-r.ctx.Done():
			return
		}
	}
}

// Deregister 注销服务
func (r *Registry) Deregister() error {
	r.cancel()

	if r.leaseID > 0 {
		_, _ = r.lease.Revoke(context.Background(), r.leaseID)
	}

	if _, err := r.client.Delete(context.Background(), r.opts.ServiceKey()); err != nil {
		return fmt.Errorf("deregister service: %w", err)
	}

	// 关闭 etcd 客户端
	if err := r.client.Close(); err != nil {
		return fmt.Errorf("close etcd client: %w", err)
	}

	return nil
}
