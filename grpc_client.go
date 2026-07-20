package core

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xs23933/core/v3/etcd"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var resolverInitOnce sync.Once

var (
	ErrGRPCClientAlreadyInitialized  = errors.New("grpc client service already initialized")
	ErrGRPCClientAlreadyConfigured   = errors.New("grpc client service already configured")
	ErrGRPCClientServiceRequired     = errors.New("grpc client service name required")
	ErrGRPCClientTargetRequired      = errors.New("grpc client target required")
	ErrGRPCClientCredentialsRequired = errors.New("grpc client transport credentials required")
	ErrGRPCClientDialOptionsConflict = errors.New("configured grpc client does not accept legacy dial options")
)

// GRPCClientConfig 为一个逻辑服务配置 transport credentials。
// Core 不解析证书或服务身份，每次建立连接时只使用该凭据的副本。
type GRPCClientConfig struct {
	TransportCredentials credentials.TransportCredentials
}

// ConfigureGRPCClient 必须在指定逻辑服务首次建立连接前调用。
func (app *Core) ConfigureGRPCClient(serviceName string, config GRPCClientConfig) error {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return ErrGRPCClientServiceRequired
	}
	if config.TransportCredentials == nil {
		return ErrGRPCClientCredentialsRequired
	}

	app.mutex.Lock()
	defer app.mutex.Unlock()
	if _, initialized := app.grpcClientInitialized[serviceName]; initialized {
		return ErrGRPCClientAlreadyInitialized
	}
	if _, configured := app.grpcClientConfigs[serviceName]; configured {
		return ErrGRPCClientAlreadyConfigured
	}
	if app.grpcClientConfigs == nil {
		app.grpcClientConfigs = make(map[string]GRPCClientConfig)
	}
	app.grpcClientConfigs[serviceName] = GRPCClientConfig{
		TransportCredentials: config.TransportCredentials.Clone(),
	}
	return nil
}

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
	credentialOption, err := app.grpcClientCredentialOption(serviceName, len(opts) > 0)
	if err != nil {
		return nil, err
	}
	if app.EtcdDiscovery == nil {
		if err := app.enableEtcdDiscoveryFromConf(); err != nil {
			return nil, err
		}
	}

	resolverInitOnce.Do(func() {
		etcd.InitEtcdResolver(app.EtcdDiscovery)
	})

	opts = append(opts, credentialOption)

	return etcd.Dial(serviceName, opts...)
}

// GrpcClientAt 使用明确 target 建立连接，并与服务发现连接共用同一份每服务安全配置。
func (app *Core) GrpcClientAt(serviceName, target string) (*grpc.ClientConn, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, ErrGRPCClientTargetRequired
	}
	credentialOption, err := app.grpcClientCredentialOption(serviceName, false)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(target, credentialOption)
}

func (app *Core) grpcClientCredentialOption(serviceName string, hasLegacyOptions bool) (grpc.DialOption, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return nil, ErrGRPCClientServiceRequired
	}

	app.mutex.Lock()
	config, configured := app.grpcClientConfigs[serviceName]
	if configured && hasLegacyOptions {
		app.mutex.Unlock()
		return nil, ErrGRPCClientDialOptionsConflict
	}
	if app.grpcClientInitialized == nil {
		app.grpcClientInitialized = make(map[string]struct{})
	}
	app.grpcClientInitialized[serviceName] = struct{}{}
	app.mutex.Unlock()

	transportCredentials := credentials.TransportCredentials(insecure.NewCredentials())
	if configured {
		transportCredentials = config.TransportCredentials.Clone()
	}
	return grpc.WithTransportCredentials(transportCredentials), nil
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
