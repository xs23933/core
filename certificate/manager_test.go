package certificate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testEngine struct {
	mu      sync.Mutex
	cert    *tls.Certificate
	calls   int
	fail    bool
	blocked chan struct{}
	entered chan struct{}
}

func (e *testEngine) Obtain(ctx context.Context, host string, renew bool) (*tls.Certificate, error) {
	e.mu.Lock()
	e.calls++
	fail := e.fail
	e.mu.Unlock()
	if e.entered != nil {
		select {
		case e.entered <- struct{}{}:
		default:
		}
	}
	if e.blocked != nil {
		select {
		case <-e.blocked:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("private secret provider failure")
	}
	return e.cert, nil
}
func (e *testEngine) Load(context.Context, string) (*tls.Certificate, error) {
	return nil, errors.New("absent")
}
func (e *testEngine) HTTPHandler(next http.Handler) http.Handler { return next }
func (e *testEngine) Close() error                               { return nil }
func testCert(t *testing.T, host string, end time.Time) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: end, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}
}
func waitState(t *testing.T, m *Manager, host string, state State) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := m.Status(context.Background(), host)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == state {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	s, _ := m.Status(context.Background(), host)
	t.Fatalf("state %s, want %s", s.State, state)
	return Status{}
}
func TestManagerAuthorizationLifecycle(t *testing.T) {
	var allow atomic.Bool
	allow.Store(true)
	e := &testEngine{cert: testCert(t, "pay.example.com", time.Now().Add(90*24*time.Hour))}
	m, err := New(Config{Engine: e, Authorize: func(context.Context, string) error {
		if !allow.Load() {
			return errors.New("denied")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "pay.example.com", StateReady)
	hello := &tls.ClientHelloInfo{ServerName: "pay.example.com"}
	if _, err = m.GetCertificate(hello); err != nil {
		t.Fatal(err)
	}
	allow.Store(false)
	if _, err = m.GetCertificate(hello); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized handshake: %v", err)
	}
	if err = m.Remove(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	allow.Store(true)
	if _, err = m.GetCertificate(hello); err == nil {
		t.Fatal("removed hostname accepted")
	}
}
func TestManagerRejectsUnknownWildcardAndFailureSecrets(t *testing.T) {
	e := &testEngine{fail: true}
	m, err := New(Config{Engine: e, Authorize: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, host := range []string{"*.example.com", "localhost", "127.0.0.1", "example.com:443", "https://example.com", "a..com"} {
		if m.Ensure(context.Background(), host) == nil {
			t.Fatalf("accepted %q", host)
		}
	}
	if _, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"}); err == nil {
		t.Fatal("unknown accepted")
	}
	if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, m, "pay.example.com", StateFailed)
	if s.ErrorCode != "issuance_failed" {
		t.Fatalf("error code %q", s.ErrorCode)
	}
}
func TestRemoveDuringIssueCannotResurrect(t *testing.T) {
	e := &testEngine{cert: testCert(t, "pay.example.com", time.Now().Add(time.Hour)), blocked: make(chan struct{}), entered: make(chan struct{}, 1)}
	m, err := New(Config{Engine: e, Authorize: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	<-e.entered
	if err = m.Remove(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	close(e.blocked)
	waitState(t, m, "pay.example.com", StateRemoved)
	if _, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "pay.example.com"}); err == nil {
		t.Fatal("removed certificate returned")
	}
}

func TestRenewalFailureKeepsValidCertificateAndRetryDeadline(t *testing.T) {
	e := &testEngine{cert: testCert(t, "pay.example.com", time.Now().Add(80*time.Hour))}
	m, err := New(Config{Engine: e, CheckInterval: time.Hour, Authorize: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "pay.example.com", StateReady)
	// 将已加载证书推进续期窗口，模拟 CA 暂时不可用。
	m.mu.Lock()
	m.entries["pay.example.com"].cert = testCert(t, "pay.example.com", time.Now().Add(time.Minute))
	m.mu.Unlock()
	e.mu.Lock()
	e.fail = true
	e.mu.Unlock()
	if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, m, "pay.example.com", StateFailed)
	if s.ErrorCode != "renewal_failed" {
		t.Fatalf("code %s", s.ErrorCode)
	}
	if _, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "pay.example.com"}); err != nil {
		t.Fatal("valid certificate lost", err)
	}
	for range 10 {
		if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
			t.Fatal(err)
		}
	}
	e.mu.Lock()
	calls := e.calls
	e.mu.Unlock()
	if calls != 2 {
		t.Fatalf("retry deadline reset: %d calls", calls)
	}
}
func TestExpiredCertificateCannotServe(t *testing.T) {
	e := &testEngine{cert: testCert(t, "pay.example.com", time.Now().Add(80*time.Hour))}
	m, err := New(Config{Engine: e, Authorize: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "pay.example.com", StateReady)
	expired := testCert(t, "pay.example.com", time.Now().Add(-time.Minute))
	m.mu.Lock()
	m.entries["pay.example.com"].cert = expired
	m.entries["pay.example.com"].status.NotAfter = expired.Leaf.NotAfter
	m.mu.Unlock()
	if _, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "pay.example.com"}); err == nil {
		t.Fatal("expired certificate served")
	}
	waitState(t, m, "pay.example.com", StateExpired)
}

func TestBackgroundRenewalRechecksAuthority(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	e := &testEngine{cert: testCert(t, "pay.example.com", time.Now().Add(80*time.Hour))}
	m, err := New(Config{Engine: e, CheckInterval: 10 * time.Millisecond, Authorize: func(context.Context, string) error {
		if !allowed.Load() {
			return ErrUnauthorized
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "pay.example.com", StateReady)
	allowed.Store(false)
	m.mu.Lock()
	m.entries["pay.example.com"].cert = testCert(t, "pay.example.com", time.Now().Add(time.Minute))
	m.mu.Unlock()
	s := waitState(t, m, "pay.example.com", StateFailed)
	if s.ErrorCode != "unauthorized" {
		t.Fatalf("code %s", s.ErrorCode)
	}
	e.mu.Lock()
	calls := e.calls
	e.mu.Unlock()
	if calls != 1 {
		t.Fatalf("unauthorized renewal reached engine: %d", calls)
	}
}
func TestCloseCancelsInFlightIssuance(t *testing.T) {
	e := &testEngine{blocked: make(chan struct{}), entered: make(chan struct{}, 1)}
	m, err := New(Config{Engine: e, Authorize: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Ensure(context.Background(), "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	<-e.entered
	done := make(chan error, 1)
	go func() { done <- m.Close() }()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel engine")
	}
	if err = m.Ensure(context.Background(), "pay.example.com"); !errors.Is(err, ErrClosed) {
		t.Fatal("closed manager accepted Ensure", err)
	}
}
