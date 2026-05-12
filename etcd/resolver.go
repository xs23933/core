package etcd

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/resolver"
)

const Scheme = "etcd"

type etcdResolverBuilder struct {
	discovery *Discovery
}

type etcdResolver struct {
	ctx       context.Context
	cancel    context.CancelFunc
	cc        resolver.ClientConn
	discovery *Discovery
	rn        chan struct{}
	wg        sync.WaitGroup
}

func (e *etcdResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOptions) (resolver.Resolver, error) {
	serviceName := target.Endpoint()

	ctx, cancel := context.WithCancel(context.Background())
	r := &etcdResolver{
		ctx:       ctx,
		cancel:    cancel,
		cc:        cc,
		discovery: e.discovery,
		rn:        make(chan struct{}, 1),
	}

	// 监听服务变化
	r.discovery.OnChange(func(services []*ServiceInfo) {
		select {
		case r.rn <- struct{}{}:
		default:
		}
	})

	r.wg.Add(1)
	go r.watch(serviceName)

	// 触发初始更新
	r.resolve(serviceName)

	return r, nil
}

func (e *etcdResolverBuilder) Scheme() string {
	return Scheme
}

func (r *etcdResolver) watch(serviceName string) {
	defer r.wg.Done()

	for {
		select {
		case <-r.rn:
			r.resolve(serviceName)
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *etcdResolver) resolve(serviceName string) {
	services := r.discovery.GetServices(serviceName)

	var addrs []resolver.Address
	for _, svc := range services {
		addrs = append(addrs, resolver.Address{
			Addr:       svc.Addr,
			ServerName: svc.Name,
			Metadata:   svc.Metadata,
		})
	}

	r.cc.UpdateState(resolver.State{Addresses: addrs})
}

func (r *etcdResolver) ResolveNow(o resolver.ResolveNowOptions) {
	select {
	case r.rn <- struct{}{}:
	default:
	}
}

func (r *etcdResolver) Close() {
	r.cancel()
	r.wg.Wait()
}

// Init 初始化 etcd resolver
func InitEtcdResolver(discovery *Discovery) {
	rb := &etcdResolverBuilder{
		discovery: discovery,
	}
	resolver.Register(rb)
}

// 方便客户端使用的 Dial 方法
func Dial(serviceName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	target := fmt.Sprintf("%s:///%s", Scheme, serviceName)

	defaultOpts := []grpc.DialOption{
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
	}

	opts = append(defaultOpts, opts...)

	return grpc.Dial(target, opts...)
}
