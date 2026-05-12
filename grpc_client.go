package core

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xs23933/core/v3/etcd"
	"google.golang.org/grpc"
)

var resolverInitOnce sync.Once

func (app *Core) GrpcClient(serviceName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	if app.etcdDiscovery == nil {
		if err := app.enableEtcdDiscoveryFromConf(); err != nil {
			return nil, err
		}
	}

	// init etcd resolver once
	resolverInitOnce.Do(func() {
		etcd.InitEtcdResolver(app.etcdDiscovery)
	})

	// build target
	target := fmt.Sprintf("etcd:///%s", serviceName)

	// default options
	defaultOpts := []grpc.DialOption{
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
	}

	opts = append(defaultOpts, opts...)

	conn, err := grpc.NewClient(target, opts...)
	return conn, err
}

// enableEtcdDiscoveryFromConf 从配置自动启用
func (app *Core) enableEtcdDiscoveryFromConf() error {
	etcdConf := app.Conf.GetMap("etcd")
	if etcdConf == nil {
		return errors.New("etcd not configured")
	}

	opts := &etcd.Options{
		Endpoints:   etcdConf.GetStrings("endpoints", []string{"127.0.0.1:2379"}),
		DialTimeout: time.Duration(etcdConf.GetInt64("dialTimeout", 5)) * time.Second,
	}

	return app.EnableEtcdDiscovery(opts)
}
