package core

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xs23933/core/v3/etcd"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var resolverInitOnce sync.Once

// MustGrpcClient 创建 gRPC 客户端连接，失败则 panic
func (app *Core) MustGrpcClient(serviceName string, opts ...grpc.DialOption) *grpc.ClientConn {
	conn, err := app.GrpcClient(serviceName, opts...)
	if err != nil {
		panic(fmt.Sprintf("grpc client %s: %v", serviceName, err))
	}
	return conn
}

// GrpcClient 创建 gRPC 客户端连接（基于 etcd 服务发现）
func (app *Core) GrpcClient(serviceName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	if app.EtcdDiscovery == nil {
		if err := app.enableEtcdDiscoveryFromConf(); err != nil {
			return nil, err
		}
	}

	resolverInitOnce.Do(func() {
		etcd.InitEtcdResolver(app.EtcdDiscovery)
	})

	// 默认添加 insecure 凭证
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	return etcd.Dial(serviceName, opts...)
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
		Namespace:   etcdConf.GetString("namespace", ""),
	}

	return app.EnableEtcdDiscovery(opts)
}
