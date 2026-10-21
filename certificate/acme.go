package certificate

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"
)

// Storage 复用 CertMagic 的持久化和跨进程锁合同。
type Storage = certmagic.Storage

type acmeEngine struct {
	config            *certmagic.Config
	issuer            *certmagic.ACMEIssuer
	cache             *certmagic.Cache
	challengeListener net.Listener
	challengeServer   *http.Server
	challengeGuard    atomic.Pointer[challengeGuard]
	closeOnce         sync.Once
	closeErr          error
}

type challengeGuard struct{ handler http.Handler }

func (e *acmeEngine) setHTTPGuard(handler http.Handler) {
	e.challengeGuard.Store(&challengeGuard{handler: handler})
}

func newACMEEngine(config Config) (*acmeEngine, error) {
	storage := config.Storage
	if storage == nil {
		if config.StorageDir == "" {
			return nil, errors.New("certificate: persistent storage required")
		}
		if err := os.MkdirAll(config.StorageDir, 0700); err != nil {
			return nil, err
		}
		storage = &certmagic.FileStorage{Path: config.StorageDir}
	}
	// 预占独立回环端口，禁止 CertMagic 为 HTTP-01 自行创建无授权 listener。
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	e := &acmeEngine{challengeListener: listener}
	e.challengeServer = &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		guard := e.challengeGuard.Load()
		if guard == nil {
			http.NotFound(w, r)
			return
		}
		guard.handler.ServeHTTP(w, r)
	})}
	e.cache = certmagic.NewCache(certmagic.CacheOptions{GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return e.config, nil }, Logger: zap.NewNop()})
	e.config = certmagic.New(e.cache, certmagic.Config{Storage: storage, Logger: zap.NewNop()})
	ca := config.CA
	if ca == "" {
		ca = certmagic.LetsEncryptProductionCA
	}
	e.issuer = certmagic.NewACMEIssuer(e.config, certmagic.ACMEIssuer{CA: ca, TestCA: ca, Email: config.Email, Agreed: true, DisableTLSALPNChallenge: true, CertObtainTimeout: config.OperationTimeout, Logger: zap.NewNop()})
	// 显式覆盖构造器从包级默认值继承的策略，避免其它应用实例影响此管理器。
	e.issuer.ListenHost = "127.0.0.1"
	e.issuer.AltHTTPPort = listener.Addr().(*net.TCPAddr).Port
	e.issuer.DNS01Solver = nil
	e.issuer.DisableHTTPChallenge = false
	e.issuer.DisableDistributedSolvers = false
	e.issuer.DisableTLSALPNChallenge = true
	e.config.OnDemand = nil
	e.config.SubjectTransformer = nil
	e.config.Issuers = []certmagic.Issuer{e.issuer}
	e.config.OnEvent = func(ctx context.Context, event string, data map[string]any) error {
		if event != "cert_obtaining" && event != "cert_renewing" {
			return nil
		}
		host, ok := data["identifier"].(string)
		if !ok {
			return ErrUnauthorized
		}
		if _, err := NormalizeHostname(host); err != nil {
			return err
		}
		if err := config.Authorize(ctx, host); err != nil {
			return ErrUnauthorized
		}
		return nil
	}
	// 不向 CertMagic 的后台缓存注册证书；续期只能通过 Manager 的授权检查执行。
	go func() {
		// listener 由引擎独占；关闭时先等待 Manager 取消全部申请，再关闭 HTTP 服务。
		if serveErr := e.challengeServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			e.challengeGuard.Store(nil)
		}
	}()
	return e, nil
}
func (e *acmeEngine) Load(ctx context.Context, host string) (*tls.Certificate, error) {
	issuerKey := e.issuer.IssuerKey()
	certPEM, err := e.config.Storage.Load(ctx, certmagic.StorageKeys.SiteCert(issuerKey, host))
	if err != nil {
		return nil, err
	}
	keyPEM, err := e.config.Storage.Load(ctx, certmagic.StorageKeys.SitePrivateKey(issuerKey, host))
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if err = ParseCertificate(&cert); err != nil {
		return nil, err
	}
	return &cert, nil
}
func (e *acmeEngine) Obtain(ctx context.Context, host string, renew bool) (*tls.Certificate, error) {
	var err error
	if renew {
		err = e.config.RenewCertSync(ctx, host, false)
	} else {
		err = e.config.ObtainCertSync(ctx, host)
	}
	if err != nil {
		return nil, err
	}
	return e.Load(ctx, host)
}
func (e *acmeEngine) HTTPHandler(next http.Handler) http.Handler {
	return e.issuer.HTTPChallengeHandler(next)
}
func (e *acmeEngine) Close() error {
	e.closeOnce.Do(func() {
		e.challengeGuard.Store(nil)
		e.closeErr = e.challengeServer.Close()
		e.cache.Stop()
	})
	return e.closeErr
}
