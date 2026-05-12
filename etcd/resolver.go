package etcd

import (
	"context"
	"fmt"
	"log"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/resolver"
)

const Scheme = "etcd"

type EtcdResolverBuilder struct {
	discovery *Discovery
	once      sync.Once
}

type etcdResolver struct {
	cc          resolver.ClientConn
	discovery   *Discovery
	serviceName string

	cancelWatch func()

	ctx    context.Context
	cancel context.CancelFunc

	rn chan struct{}
	wg sync.WaitGroup
}

func InitEtcdResolver(discovery *Discovery) {
	resolver.Register(&EtcdResolverBuilder{
		discovery: discovery,
	})
}

func (b *EtcdResolverBuilder) Scheme() string {
	return Scheme
}

func (b *EtcdResolverBuilder) Build(
	target resolver.Target,
	cc resolver.ClientConn,
	opts resolver.BuildOptions,
) (resolver.Resolver, error) {

	serviceName := target.Endpoint()

	if err := b.discovery.Watch(serviceName); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	r := &etcdResolver{
		cc:          cc,
		discovery:   b.discovery,
		serviceName: serviceName,
		ctx:         ctx,
		cancel:      cancel,
		rn:          make(chan struct{}, 1),
	}

	r.cancelWatch = b.discovery.Subscribe(serviceName, func() {
		select {
		case r.rn <- struct{}{}:
		default:
		}
	})

	r.wg.Add(1)
	go r.watchLoop()

	r.resolve()

	return r, nil
}

func (r *etcdResolver) watchLoop() {
	defer r.wg.Done()

	for {
		select {

		case <-r.rn:
			r.resolve()

		case <-r.ctx.Done():
			return
		}
	}
}

func (r *etcdResolver) resolve() {
	services := r.discovery.GetServices(r.serviceName)

	// ✅ 添加调试日志
	log.Printf("[DEBUG] resolve service: %s, found %d instances", r.serviceName, len(services))

	for _, svc := range services {
		log.Printf("[DEBUG]   instance: %s (ID: %s)", svc.Addr, svc.ID)
	}

	addrs := make([]resolver.Address, 0, len(services))

	for _, svc := range services {
		addrs = append(addrs, resolver.Address{
			Addr: svc.Addr,
		})
	}

	if len(addrs) == 0 {
		log.Printf("[WARN] no instances found for service: %s", r.serviceName)

		r.cc.UpdateState(resolver.State{
			Addresses: []resolver.Address{},
		})
		return
	}

	log.Printf("[INFO] updating state with %d addresses for service: %s", len(addrs), r.serviceName)

	r.cc.UpdateState(resolver.State{
		Addresses: addrs,
	})
}

func (r *etcdResolver) ResolveNow(o resolver.ResolveNowOptions) {
	select {
	case r.rn <- struct{}{}:
	default:
	}
}

func (r *etcdResolver) Close() {
	r.cancel()

	if r.cancelWatch != nil {
		r.cancelWatch()
	}

	r.wg.Wait()
}

func Dial(serviceName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	target := fmt.Sprintf("%s:///%s", Scheme, serviceName)

	defaultOpts := []grpc.DialOption{
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
	}

	opts = append(defaultOpts, opts...)

	return grpc.NewClient(target, opts...)
}
