package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xs23933/core/v3/workload"
	"google.golang.org/grpc/credentials"
)

// workloadRuntime 将进程身份、签发器和一次性初始化串行化。
type workloadRuntime struct {
	mu       sync.Mutex
	identity *workload.Identity
	issuer   *workload.Issuer
}

// EnableWorkloadIdentity 在首次创建 gRPC 服务端或客户端前准备本进程身份。
// 未启用配置时不改变既有 transport；启用后任何初始化失败都拒绝明文回退。
func (app *Core) EnableWorkloadIdentity() error {
	if !app.Conf.GetBool("workload.enabled", false) {
		return nil
	}
	app.mutex.Lock()
	if app.workload == nil {
		app.workload = &workloadRuntime{}
	}
	runtime := app.workload
	app.mutex.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.identity != nil {
		if !runtime.identity.Valid() {
			return errors.New("core: workload identity expired")
		}
		return nil
	}
	app.mutex.Lock()
	late := app.grpcServer != nil || len(app.grpcClientInitialized) != 0 || app.shutdownStarted
	conflict := len(app.grpcClientConfigs) != 0 || (app.grpcConfig != nil && app.grpcConfig.TransportCredentials != nil)
	app.mutex.Unlock()
	if late {
		return errors.New("core: workload identity must be initialized before grpc and shutdown")
	}
	if conflict {
		return errors.New("core: managed workload conflicts with explicit transport")
	}
	c := workload.Config{
		ServiceName:        app.Conf.GetString("workload.service_name", app.Conf.GetString("etcd.service_name")),
		TrustDomain:        app.Conf.GetString("workload.trust_domain"),
		IssuerURL:          app.Conf.GetString("workload.issuer_url"),
		TrustBundleFile:    app.Conf.GetString("workload.trust_bundle_file"),
		RootSHA256:         app.Conf.GetString("workload.root_sha256"),
		BootstrapTokenFile: app.Conf.GetString("workload.bootstrap_token_file"),
		StateDir:           app.Conf.GetString("workload.state_dir"),
		RenewBefore:        app.Conf.GetDuration("workload.renew_before", 0),
		RetryInterval:      app.Conf.GetDuration("workload.retry_interval", time.Second),
	}
	var issuer *workload.Issuer
	var identity *workload.Identity
	var err error
	if app.Conf.GetBool("workload_issuer.enabled", false) {
		issuer, err = workload.OpenIssuer(workload.IssuerConfig{
			StateDir:      app.Conf.GetString("workload_issuer.state_dir"),
			TrustDomain:   c.TrustDomain,
			ListenAddress: app.Conf.GetString("workload_issuer.listen"),
			AdvertiseURL:  app.Conf.GetString("workload_issuer.advertise_url", c.IssuerURL),
			PolicyFile:    app.Conf.GetString("workload_issuer.policy_file"),
			PinFile:       app.Conf.GetString("workload_issuer.pin_file"),
			LeafTTL:       app.Conf.GetDuration("workload_issuer.leaf_ttl", 7*24*time.Hour),
		})
		if err != nil {
			return fmt.Errorf("core: initialize workload issuer: %w", err)
		}
		identity, err = workload.OpenLocal(app.Ctx, c, issuer)
		if err == nil {
			err = issuer.Start(app.Ctx)
		}
	} else {
		identity, err = workload.Open(app.Ctx, c)
	}
	if err != nil {
		if identity != nil {
			_ = identity.Close()
		}
		if issuer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = issuer.Close(ctx)
			cancel()
		}
		return fmt.Errorf("core: initialize workload identity: %w", err)
	}
	app.mutex.Lock()
	if app.shutdownStarted {
		app.mutex.Unlock()
		_ = identity.Close()
		if issuer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = issuer.Close(ctx)
			cancel()
		}
		return errors.New("core: shutdown began during workload initialization")
	}
	runtime.identity, runtime.issuer = identity, issuer
	app.mutex.Unlock()
	app.OnShutdown(func() {
		_ = identity.Close()
		if issuer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = issuer.Close(ctx)
		}
	})
	go func() {
		select {
		case <-app.Ctx.Done():
		case <-identity.Expired():
			// 到期不可依靠旧连接无限延续权限；先撤注册，再在期限内关闭所有传输。
			_ = app.ShutdownWithTimeout(5 * time.Second)
		}
	}()
	return nil
}

func (app *Core) workloadIdentity() (*workload.Identity, error) {
	if !app.Conf.GetBool("workload.enabled", false) {
		return nil, errors.New("core: managed workload is disabled")
	}
	app.mutex.Lock()
	runtime := app.workload
	if runtime == nil {
		app.mutex.Unlock()
		return nil, errors.New("core: call EnableWorkloadIdentity before grpc")
	}
	identity := runtime.identity
	app.mutex.Unlock()
	if identity == nil || !identity.Valid() {
		return nil, errors.New("core: workload identity is unavailable")
	}
	return identity, nil
}

// WorkloadServerCredentials 为专用内部 listener 复用当前进程的动态身份。
func (app *Core) WorkloadServerCredentials() (credentials.TransportCredentials, error) {
	identity, err := app.workloadIdentity()
	if err != nil {
		return nil, err
	}
	return identity.ServerCredentials(), nil
}

// WorkloadClientCredentials 为专用内部客户端复用身份并验证目标逻辑服务。
func (app *Core) WorkloadClientCredentials(target string) (credentials.TransportCredentials, error) {
	identity, err := app.workloadIdentity()
	if err != nil {
		return nil, err
	}
	if target == "" {
		return nil, ErrGRPCClientServiceRequired
	}
	return identity.ClientCredentials(target), nil
}

// WorkloadIdentityStats 返回不含凭据的本进程申请与有效期快照。
func (app *Core) WorkloadIdentityStats() (workload.Stats, error) {
	app.mutex.Lock()
	runtime := app.workload
	var identity *workload.Identity
	if runtime != nil {
		identity = runtime.identity
	}
	app.mutex.Unlock()
	if identity == nil {
		return workload.Stats{}, errors.New("core: workload identity not initialized")
	}
	return identity.Stats(), nil
}

// WorkloadIssuerStats 返回签发器的无秘密统计，不把 CA 状态暴露到普通业务接口。
func (app *Core) WorkloadIssuerStats() (workload.IssuerStats, error) {
	app.mutex.Lock()
	runtime := app.workload
	var issuer *workload.Issuer
	if runtime != nil {
		issuer = runtime.issuer
	}
	app.mutex.Unlock()
	if issuer == nil {
		return workload.IssuerStats{}, errors.New("core: workload issuer not initialized")
	}
	return issuer.Stats(), nil
}

// RotateWorkloadIntermediate 供受控运维更新中间 CA，不改变根信任；根私钥离线时明确拒绝。
func (app *Core) RotateWorkloadIntermediate() error {
	app.mutex.Lock()
	runtime := app.workload
	var issuer *workload.Issuer
	if runtime != nil && !app.shutdownStarted {
		issuer = runtime.issuer
	}
	app.mutex.Unlock()
	if issuer == nil {
		return errors.New("core: workload issuer unavailable")
	}
	return issuer.RotateIntermediate()
}
