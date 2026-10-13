package workload

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Config 描述进程级工作负载身份，不包含逐目标服务凭据。
type Config struct {
	ServiceName, TrustDomain, IssuerURL, TrustBundleFile, BootstrapTokenFile, StateDir, RootSHA256 string
	RenewBefore, RetryInterval                                                                     time.Duration
}

type identityState struct {
	KeyPEM         []byte `json:"key_pem"`
	CertificatePEM []byte `json:"certificate_pem,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
	CSRPEM         []byte `json:"csr_pem,omitempty"`
}

// Identity 持有可轮换的本进程身份；副本不得共享状态目录。
type Identity struct {
	conf       Config
	attempts   atomic.Uint64
	successes  atomic.Uint64
	failures   atomic.Uint64
	roots      *x509.CertPool
	mu         sync.RWMutex
	cert       *tls.Certificate
	closed     bool
	state      identityState
	lock       *os.File
	cancel     context.CancelFunc
	done       chan struct{}
	expired    chan struct{}
	expireOnce sync.Once
	local      func(string, []byte, string) ([]byte, []byte, error)
}

// Open 校验本地身份或通过可信 HTTPS 申请，并启动后台续期。
func Open(ctx context.Context, conf Config) (*Identity, error) { return openIdentity(ctx, conf, nil) }

func openIdentity(ctx context.Context, conf Config, local func(string, []byte, string) ([]byte, []byte, error)) (*Identity, error) {
	if _, err := workloadURI(conf.TrustDomain, conf.ServiceName); err != nil {
		return nil, err
	}
	if conf.StateDir == "" {
		return nil, errors.New("workload state_dir required")
	}
	if conf.RetryInterval <= 0 {
		conf.RetryInterval = time.Minute
	}
	if err := os.MkdirAll(conf.StateDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Stat(conf.StateDir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("workload state directory must have mode 0700")
	}
	f, err := os.OpenFile(filepath.Join(conf.StateDir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("workload state directory is already in use")
	}
	id := &Identity{conf: conf, lock: f, done: make(chan struct{}), expired: make(chan struct{}), local: local}
	cleanup := func(err error) (*Identity, error) {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, err
	}
	rootsPEM, err := loadTrustBundle(ctx, conf, local == nil)
	if err != nil {
		return cleanup(err)
	}
	id.roots = x509.NewCertPool()
	if !id.roots.AppendCertsFromPEM(rootsPEM) {
		return cleanup(errors.New("invalid workload trust bundle"))
	}
	data, err := os.ReadFile(filepath.Join(conf.StateDir, "identity.json"))
	if err == nil {
		info, statErr := os.Stat(filepath.Join(conf.StateDir, "identity.json"))
		if statErr != nil {
			return cleanup(statErr)
		}
		if info.Mode().Perm()&0077 != 0 {
			return cleanup(errors.New("workload identity must have mode 0600"))
		}
		if err = json.Unmarshal(data, &id.state); err != nil {
			return cleanup(errors.New("corrupt workload identity state; restore protected backup"))
		}
		if len(id.state.KeyPEM) == 0 {
			return cleanup(errors.New("workload identity key missing"))
		}
		if len(id.state.CertificatePEM) > 0 {
			cert, e := id.parseCertificate(id.state.CertificatePEM, false)
			if e != nil {
				return cleanup(e)
			}
			id.cert = cert
		}
	} else if !os.IsNotExist(err) {
		return cleanup(err)
	}
	if len(id.state.KeyPEM) == 0 {
		key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return cleanup(e)
		}
		der, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return cleanup(e)
		}
		id.state.KeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if e = id.saveState(); e != nil {
			return cleanup(e)
		}
	}
	if !id.Valid() {
		if err = id.refresh(ctx); err != nil {
			return cleanup(fmt.Errorf("workload enrollment failed: %w", err))
		}
	}
	bg, cancel := context.WithCancel(ctx)
	id.cancel = cancel
	go id.maintain(bg)
	return id, nil
}

func (i *Identity) saveState() error {
	data, err := json.Marshal(i.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(i.conf.StateDir, ".identity-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(i.conf.StateDir, "identity.json")); err != nil {
		return err
	}
	dir, err := os.Open(i.conf.StateDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (i *Identity) parseCertificate(data []byte, requireCurrent bool) (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair(data, i.state.KeyPEM)
	if err != nil {
		return nil, errors.New("invalid workload certificate/key pair")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	cert.Leaf = leaf
	now := time.Now()
	if !requireCurrent && now.After(leaf.NotAfter) {
		now = leaf.NotAfter.Add(-time.Second)
	}
	if err = verifyWorkload(cert.Certificate, i.roots, i.conf.TrustDomain, i.conf.ServiceName, x509.ExtKeyUsageServerAuth, now); err != nil {
		return nil, err
	}
	if err = verifyWorkload(cert.Certificate, i.roots, i.conf.TrustDomain, i.conf.ServiceName, x509.ExtKeyUsageClientAuth, now); err != nil {
		return nil, err
	}
	return &cert, nil
}

// ExpiresAt 返回当前叶子证书的失效时间。
func (i *Identity) ExpiresAt() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.cert == nil {
		return time.Time{}
	}
	return i.cert.Leaf.NotAfter
}

// Valid 表示当前身份可用于新握手。
func (i *Identity) Valid() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return !i.closed && i.cert != nil && !time.Now().Before(i.cert.Leaf.NotBefore) && time.Now().Before(i.cert.Leaf.NotAfter)
}

// Expired 在续期未成功且身份到期时关闭，调用方应撤回就绪并排空连接。
func (i *Identity) Expired() <-chan struct{} { return i.expired }

// Close 停止续期后释放状态目录锁。
func (i *Identity) Close() error {
	if i.cancel == nil {
		return nil
	}
	i.cancel()
	<-i.done
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closed = true
	if i.lock == nil {
		return nil
	}
	err := syscall.Flock(int(i.lock.Fd()), syscall.LOCK_UN)
	closeErr := i.lock.Close()
	i.lock = nil
	if err != nil {
		return err
	}
	return closeErr
}

func (i *Identity) maintain(ctx context.Context) {
	defer close(i.done)
	expiryDone := make(chan struct{})
	go func() { defer close(expiryDone); i.watchExpiry(ctx) }()
	defer func() { <-expiryDone }()
	retry := i.conf.RetryInterval
	for {
		i.mu.RLock()
		leaf := i.cert.Leaf
		i.mu.RUnlock()
		before := i.conf.RenewBefore
		lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
		if before <= 0 || before >= lifetime {
			before = lifetime / 2
		}
		// 每个实例提前一个有界随机窗口续期，分散签发器负载。
		var jitterBytes [8]byte
		if _, err := rand.Read(jitterBytes[:]); err == nil {
			window := lifetime / 20
			if window > 0 {
				before += time.Duration(binary.BigEndian.Uint64(jitterBytes[:]) % uint64(window))
			}
		}
		delay := time.Until(leaf.NotAfter.Add(-before))
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := i.refresh(ctx); err == nil {
			retry = i.conf.RetryInterval
			continue
		}
		if !i.Valid() {
			i.expireOnce.Do(func() { close(i.expired) })
		}
		wait := retry
		if until := time.Until(i.ExpiresAt()); until > 0 && until < wait {
			wait = until
		}
		timer = time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !i.Valid() {
			i.expireOnce.Do(func() { close(i.expired) })
		}
		if retry < 15*time.Minute {
			retry *= 2
		}
	}
}

// watchExpiry 独立于网络续期，避免挂起的申请延迟撤回就绪状态。
func (i *Identity) watchExpiry(ctx context.Context) {
	for {
		remaining := time.Until(i.ExpiresAt())
		if remaining <= 0 {
			select {
			case <-ctx.Done():
				return
			default:
			}
			i.expireOnce.Do(func() { close(i.expired) })
			return
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Stats 是不含身份密钥或引导令牌的只读运行快照，签发计数包含首次申请和续期。
type Stats struct {
	Attempts, Successes, Failures uint64
	ExpiresAt                     time.Time
	Remaining                     time.Duration
}

// Stats 返回可接入监控的申请结果和证书剩余寿命。
func (i *Identity) Stats() Stats {
	expiry := i.ExpiresAt()
	remaining := time.Until(expiry)
	if remaining < 0 {
		remaining = 0
	}
	return Stats{Attempts: i.attempts.Load(), Successes: i.successes.Load(), Failures: i.failures.Load(), ExpiresAt: expiry, Remaining: remaining}
}
