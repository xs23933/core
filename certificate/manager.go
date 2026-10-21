// Package certificate 提供由业务授权的精确域名证书管理，不在握手中触发签发。
package certificate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrUnauthorized = errors.New("certificate: hostname unauthorized")
	ErrUnknown      = errors.New("certificate: hostname not managed")
	ErrUnavailable  = errors.New("certificate: certificate unavailable")
	ErrClosed       = errors.New("certificate: manager closed")
)

type State string

const (
	StatePending  State = "pending"
	StateIssuing  State = "issuing"
	StateReady    State = "ready"
	StateRenewing State = "renewing"
	StateFailed   State = "failed"
	StateExpired  State = "expired"
	StateRemoved  State = "removed"
)

// Status 仅包含可公开的观测信息，不包含私钥或底层错误原文。
type Status struct {
	Hostname  string    `json:"hostname"`
	State     State     `json:"state"`
	ErrorCode string    `json:"error_code,omitempty"`
	NotBefore time.Time `json:"not_before,omitempty"`
	NotAfter  time.Time `json:"not_after,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Engine 是签发与专用存储边界；实现必须遵守取消并返回精确域名证书。
type Engine interface {
	Load(context.Context, string) (*tls.Certificate, error)
	Obtain(context.Context, string, bool) (*tls.Certificate, error)
	HTTPHandler(http.Handler) http.Handler
	Close() error
}

type Config struct {
	StorageDir       string
	Storage          Storage
	Email            string
	CA               string
	Authorize        func(context.Context, string) error
	Engine           Engine
	CheckInterval    time.Duration
	OperationTimeout time.Duration
}

type entry struct {
	status     Status
	cert       *tls.Certificate
	running    bool
	cancel     context.CancelFunc
	retryAfter time.Time
}
type Manager struct {
	mu        sync.RWMutex
	entries   map[string]*entry
	config    Config
	engine    Engine
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func New(config Config) (*Manager, error) {
	if config.Authorize == nil {
		return nil, errors.New("certificate: explicit authorization callback required")
	}
	if config.CheckInterval <= 0 {
		config.CheckInterval = time.Minute
	}
	if config.OperationTimeout <= 0 {
		config.OperationTimeout = 5 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{entries: map[string]*entry{}, config: config, ctx: ctx, cancel: cancel}
	m.engine = config.Engine
	if m.engine == nil {
		e, err := newACMEEngine(config)
		if err != nil {
			cancel()
			return nil, err
		}
		m.engine = e
	}
	// 内置引擎的所有 HTTP-01 listener 均通过相同的成员、撤销和业务授权检查。
	if engine, ok := m.engine.(interface{ setHTTPGuard(http.Handler) }); ok {
		engine.setHTTPGuard(m.HTTPHandler(nil))
	}
	m.wg.Add(1)
	go m.maintain()
	return m, nil
}

// NormalizeHostname 仅接受精确 ASCII DNS 名称；调用方应先将国际化域名转换为 IDNA。
func NormalizeHostname(host string) (string, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return "", ErrUnauthorized
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrUnauthorized
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", ErrUnauthorized
			}
		}
	}
	return host, nil
}
func (m *Manager) authorize(ctx context.Context, host string) error {
	if err := m.config.Authorize(ctx, host); err != nil {
		return ErrUnauthorized
	}
	return nil
}
func (m *Manager) Ensure(ctx context.Context, hostname string) error {
	host, err := NormalizeHostname(hostname)
	if err != nil {
		return err
	}
	if err = m.authorize(ctx, host); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	e := m.entries[host]
	if e == nil || e.status.State == StateRemoved {
		e = &entry{status: Status{Hostname: host, State: StatePending, UpdatedAt: time.Now().UTC()}}
		m.entries[host] = e
	}
	if !e.running && (e.cert == nil || needsRenewal(e.cert, time.Now())) && !time.Now().Before(e.retryAfter) {
		m.startLocked(host, e)
	}
	return nil
}
func (m *Manager) startLocked(host string, e *entry) {
	e.running = true
	ctx, cancel := context.WithTimeout(m.ctx, m.config.OperationTimeout)
	e.cancel = cancel
	m.wg.Add(1)
	go func() { defer m.wg.Done(); defer cancel(); m.obtain(ctx, host, e) }()
}
func (m *Manager) obtain(ctx context.Context, host string, e *entry) {
	if err := m.authorize(ctx, host); err != nil {
		m.finish(host, e, nil, "unauthorized")
		return
	}
	m.mu.RLock()
	cert := e.cert
	m.mu.RUnlock()
	if cert == nil {
		loaded, err := m.engine.Load(ctx, host)
		if err == nil {
			cert = loaded
		}
	}
	if validCertificate(cert, host, time.Now()) == nil && !needsRenewal(cert, time.Now()) {
		m.finish(host, e, cert, "")
		return
	}
	m.mu.Lock()
	if m.entries[host] != e || e.status.State == StateRemoved {
		m.mu.Unlock()
		return
	}
	renew := cert != nil
	if cert != nil && cert.Leaf != nil {
		e.status.NotBefore = cert.Leaf.NotBefore
		e.status.NotAfter = cert.Leaf.NotAfter
		if validCertificate(cert, host, time.Now()) == nil {
			e.cert = cert
		}
	}
	if renew {
		e.status.State = StateRenewing
	} else {
		e.status.State = StateIssuing
	}
	e.status.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()
	if err := m.authorize(ctx, host); err != nil {
		m.finish(host, e, nil, "unauthorized")
		return
	}
	result, err := m.engine.Obtain(ctx, host, renew)
	code := ""
	if err != nil || validCertificate(result, host, time.Now()) != nil {
		if renew {
			code = "renewal_failed"
		} else {
			code = "issuance_failed"
		}
		result = nil
	}
	if m.authorize(ctx, host) != nil {
		result = nil
		code = "unauthorized"
	}
	m.finish(host, e, result, code)
}
func (m *Manager) finish(host string, e *entry, cert *tls.Certificate, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.entries[host] != e || e.status.State == StateRemoved {
		return
	}
	e.running = false
	e.cancel = nil
	e.status.UpdatedAt = time.Now().UTC()
	e.status.ErrorCode = code
	if cert != nil {
		e.cert = cert
		e.status.NotBefore = cert.Leaf.NotBefore
		e.status.NotAfter = cert.Leaf.NotAfter
		e.status.State = StateReady
		e.retryAfter = time.Time{}
	} else {
		e.status.State = StateFailed
		e.retryAfter = time.Now().Add(m.config.CheckInterval)
	}
}
func validCertificate(cert *tls.Certificate, host string, now time.Time) error {
	if cert == nil || cert.Leaf == nil {
		return ErrUnavailable
	}
	if len(cert.Leaf.DNSNames) != 1 || cert.Leaf.DNSNames[0] != host || cert.Leaf.VerifyHostname(host) != nil || now.Before(cert.Leaf.NotBefore) || !now.Before(cert.Leaf.NotAfter) {
		return ErrUnavailable
	}
	return nil
}
func needsRenewal(cert *tls.Certificate, now time.Time) bool {
	if cert == nil || cert.Leaf == nil {
		return true
	}
	return now.After(cert.Leaf.NotAfter.Add(-cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore) / 3))
}
func (m *Manager) Remove(ctx context.Context, hostname string) error {
	host, err := NormalizeHostname(hostname)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	e := m.entries[host]
	if e == nil {
		e = &entry{}
		m.entries[host] = e
	}
	if e.cancel != nil {
		e.cancel()
	}
	e.cert = nil
	e.status = Status{Hostname: host, State: StateRemoved, UpdatedAt: time.Now().UTC()}
	return nil
}
func (m *Manager) Status(ctx context.Context, hostname string) (Status, error) {
	host, err := NormalizeHostname(hostname)
	if err != nil {
		return Status{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return Status{}, ErrClosed
	}
	e := m.entries[host]
	if e == nil {
		return Status{}, ErrUnknown
	}
	s := e.status
	if !s.NotAfter.IsZero() && !time.Now().Before(s.NotAfter) && s.State != StateRemoved {
		s.State = StateExpired
	}
	return s, nil
}
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello == nil {
		return nil, ErrUnknown
	}
	host, err := NormalizeHostname(hello.ServerName)
	if err != nil {
		return nil, err
	}
	ctx := hello.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err = m.authorize(ctx, host); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrClosed
	}
	e := m.entries[host]
	if e == nil || e.status.State == StateRemoved {
		return nil, ErrUnknown
	}
	if err = validCertificate(e.cert, host, time.Now()); err != nil {
		return nil, err
	}
	return e.cert, nil
}
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: m.GetCertificate, NextProtos: []string{"h2", "http/1.1"}, SessionTicketsDisabled: true}
}
func (m *Manager) HTTPHandler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	handler := m.engine.HTTPHandler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			host := r.Host
			if name, _, err := net.SplitHostPort(host); err == nil {
				host = name
			}
			host, err := NormalizeHostname(host)
			if err != nil || m.authorize(r.Context(), host) != nil {
				http.NotFound(w, r)
				return
			}
			m.mu.RLock()
			e := m.entries[host]
			allowed := !m.closed && e != nil && e.status.State != StateRemoved
			m.mu.RUnlock()
			if !allowed {
				http.NotFound(w, r)
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
}
func (m *Manager) maintain() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			for host, e := range m.entries {
				if e.status.State != StateRemoved && !e.running && !time.Now().Before(e.retryAfter) && (e.cert == nil || needsRenewal(e.cert, time.Now())) {
					m.startLocked(host, e)
				}
			}
			m.mu.Unlock()
		}
	}
}
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		m.mu.Unlock()
		m.wg.Wait()
		m.closeErr = m.engine.Close()
	})
	return m.closeErr
}

// ParseCertificate 将标准 TLS 证书链解析为可验证的观测叶证书。
func ParseCertificate(cert *tls.Certificate) error {
	if cert == nil || len(cert.Certificate) == 0 {
		return ErrUnavailable
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	cert.Leaf = leaf
	return nil
}
