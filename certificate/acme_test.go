package certificate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/mholt/acmez/v3/acme"
)

func TestACMELoadPersistentCertificateWithoutIssuance(t *testing.T) {
	dir := t.TempDir()
	config := Config{StorageDir: dir, CA: "https://ca.invalid/directory", Authorize: func(context.Context, string) error { return nil }, OperationTimeout: time.Second}
	engine, err := newACMEEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	cert := testCert(t, "pay.example.com", time.Now().Add(90*24*time.Hour))
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	issuer := engine.issuer.IssuerKey()
	if err = engine.config.Storage.Store(ctx, certmagic.StorageKeys.SiteCert(issuer, "pay.example.com"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})); err != nil {
		t.Fatal(err)
	}
	if err = engine.config.Storage.Store(ctx, certmagic.StorageKeys.SitePrivateKey(issuer, "pay.example.com"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})); err != nil {
		t.Fatal(err)
	}
	if err = engine.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err = manager.Ensure(ctx, "pay.example.com"); err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, "pay.example.com", StateReady)
}
func TestHTTPChallengeAuthorizationAndFallback(t *testing.T) {
	m, err := New(Config{Engine: &testEngine{}, Authorize: func(context.Context, string) error { return ErrUnauthorized }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	handler := m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tt := range []struct {
		path string
		code int
	}{{"/health", 204}, {"/.well-known/acme-challenge/token", 404}} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "http://pay.example.com"+tt.path, nil))
		if rec.Code != tt.code {
			t.Fatalf("%s: %d", tt.path, rec.Code)
		}
	}
}

// challengeFixtureEngine 仅阻塞证书加载以维持待申请状态；挑战仍经过真实 CertMagic handler。
type challengeFixtureEngine struct {
	*acmeEngine
	entered     chan struct{}
	obtainCalls atomic.Int32
}

func (e *challengeFixtureEngine) Load(ctx context.Context, _ string) (*tls.Certificate, error) {
	select {
	case e.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (e *challengeFixtureEngine) Obtain(context.Context, string, bool) (*tls.Certificate, error) {
	e.obtainCalls.Add(1)
	return nil, errors.New("offline fixture prohibits certificate issuance")
}

func TestDistributedHTTPChallengeUsesSharedStorageAndCurrentAuthorization(t *testing.T) {
	const host = "pay-distributed.example.com"
	const unknown = "unknown-distributed.example.com"
	const token = "offline_distributed_token"
	const keyAuthorization = token + ".offline-account-thumbprint"
	ctx := context.Background()
	dir := t.TempDir()
	var revoked atomic.Bool
	authorize := func(ctx context.Context, hostname string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if revoked.Load() && hostname == host {
			return ErrUnauthorized
		}
		return nil
	}
	var managers []*Manager
	var engines []*challengeFixtureEngine
	for range 2 {
		config := Config{StorageDir: dir, CA: "https://offline-ca.invalid/directory", Authorize: authorize, OperationTimeout: time.Minute}
		engine, err := newACMEEngine(config)
		if err != nil {
			t.Fatal(err)
		}
		fixture := &challengeFixtureEngine{acmeEngine: engine, entered: make(chan struct{}, 1)}
		config.Engine = fixture
		manager, err := New(config)
		if err != nil {
			if closeErr := engine.Close(); closeErr != nil {
				t.Error(closeErr)
			}
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := manager.Close(); err != nil {
				t.Error(err)
			}
		})
		if err = manager.Ensure(ctx, host); err != nil {
			t.Fatal(err)
		}
		select {
		case <-fixture.entered:
		case <-time.After(time.Second):
			t.Fatal("fixture load did not start")
		}
		waitState(t, manager, host, StatePending)
		managers = append(managers, manager)
		engines = append(engines, fixture)
	}
	if _, present := certmagic.GetACMEChallenge(host); present {
		t.Fatal("challenge unexpectedly present in process memory")
	}
	// 存储格式来自 CertMagic distributedSolver.Present：acme/<issuer>/challenge_tokens/<hostname>.json。
	storeChallenge := func(hostname string) {
		t.Helper()
		challenge := acme.Challenge{Type: acme.ChallengeTypeHTTP01, Token: token, KeyAuthorization: keyAuthorization, Identifier: acme.Identifier{Type: "dns", Value: hostname}}
		data, err := json.Marshal(challenge)
		if err != nil {
			t.Fatal(err)
		}
		storageKey := path.Join("acme", certmagic.StorageKeys.Safe(engines[0].issuer.IssuerKey()), "challenge_tokens", certmagic.StorageKeys.Safe(hostname)+".json")
		if err = engines[0].config.Storage.Store(ctx, storageKey, data); err != nil {
			t.Fatal(err)
		}
	}
	storeChallenge(host)
	storeChallenge(unknown)
	handler := managers[1].HTTPHandler(nil)
	request := func(requestContext context.Context, hostname, requestToken string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://"+hostname+"/.well-known/acme-challenge/"+requestToken, nil).WithContext(requestContext)
		handler.ServeHTTP(rec, req)
		return rec
	}
	success := request(ctx, host, token)
	if success.Code != http.StatusOK || success.Body.String() != keyAuthorization {
		t.Fatalf("distributed response: status=%d body=%q", success.Code, success.Body.String())
	}
	if success.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("challenge content type %q", success.Header().Get("Content-Type"))
	}
	if _, present := certmagic.GetACMEChallenge(host); present {
		t.Fatal("challenge was solved from an unexpected in-memory entry")
	}
	if rec := request(ctx, host, "incorrect-token"); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong token accepted: %d", rec.Code)
	}
	// 即使共享存储中存在合法挑战且回调允许，也必须先由本实例 Ensure 管理该域名。
	if rec := request(ctx, unknown, token); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown hostname accepted: %d", rec.Code)
	}
	revoked.Store(true)
	if rec := request(ctx, host, token); rec.Code != http.StatusNotFound || rec.Body.String() == keyAuthorization {
		t.Fatalf("revoked challenge leaked: %d %q", rec.Code, rec.Body.String())
	}
	revoked.Store(false)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if rec := request(canceled, host, token); rec.Code != http.StatusNotFound {
		t.Fatalf("canceled challenge accepted: %d", rec.Code)
	}
	if err := managers[1].Remove(ctx, host); err != nil {
		t.Fatal(err)
	}
	if rec := request(ctx, host, token); rec.Code != http.StatusNotFound {
		t.Fatalf("removed hostname accepted: %d", rec.Code)
	}
	for _, manager := range managers {
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, engine := range engines {
		if engine.obtainCalls.Load() != 0 {
			t.Fatal("offline challenge fixture unexpectedly attempted issuance")
		}
	}
}

func TestAutomaticSolverListenerAlwaysUsesManagerAuthorization(t *testing.T) {
	const host = "automatic-guard.example.com"
	const unknown = "automatic-unknown.example.com"
	const token = "automatic_guard_token"
	var revoked atomic.Bool
	config := Config{StorageDir: t.TempDir(), CA: "https://offline-ca.invalid/directory", Authorize: func(ctx context.Context, hostname string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if revoked.Load() {
			return ErrUnauthorized
		}
		return nil
	}, OperationTimeout: time.Minute}
	engine, err := newACMEEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &challengeFixtureEngine{acmeEngine: engine, entered: make(chan struct{}, 1)}
	config.Engine = fixture
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	if engine.issuer.ListenHost != "127.0.0.1" || engine.issuer.AltHTTPPort <= 0 {
		t.Fatalf("automatic solver has an unguarded address %s:%d", engine.issuer.ListenHost, engine.issuer.AltHTTPPort)
	}
	address := net.JoinHostPort(engine.issuer.ListenHost, strconv.Itoa(engine.issuer.AltHTTPPort))
	if address != engine.challengeListener.Addr().String() {
		t.Fatalf("solver address %s is not prebound guarded address %s", address, engine.challengeListener.Addr())
	}
	storeChallenge := func(hostname string) {
		t.Helper()
		data, marshalErr := json.Marshal(acme.Challenge{Type: acme.ChallengeTypeHTTP01, Token: token, KeyAuthorization: token + ".offline-key", Identifier: acme.Identifier{Type: "dns", Value: hostname}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		key := path.Join("acme", certmagic.StorageKeys.Safe(engine.issuer.IssuerKey()), "challenge_tokens", certmagic.StorageKeys.Safe(hostname)+".json")
		if storeErr := engine.config.Storage.Store(context.Background(), key, data); storeErr != nil {
			t.Fatal(storeErr)
		}
	}
	storeChallenge(host)
	storeChallenge(unknown)
	client := &http.Client{Timeout: time.Second}
	request := func(hostname string) (int, string) {
		t.Helper()
		req, requestErr := http.NewRequest("GET", "http://"+address+"/.well-known/acme-challenge/"+token, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Host = hostname
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer resp.Body.Close()
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return resp.StatusCode, string(body)
	}
	if status, _ := request(host); status != 404 {
		t.Fatal("unmanaged host reached automatic solver")
	}
	if err = manager.Ensure(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	<-fixture.entered
	if status, body := request(host); status != 200 || body != token+".offline-key" {
		t.Fatalf("guarded automatic response %d %q", status, body)
	}
	if status, _ := request(unknown); status != 404 {
		t.Fatal("unknown host reached automatic solver")
	}
	revoked.Store(true)
	if status, _ := request(host); status != 404 {
		t.Fatal("revoked host reached automatic solver")
	}
	revoked.Store(false)
	if err = manager.Remove(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	if status, _ := request(host); status != 404 {
		t.Fatal("removed host reached automatic solver")
	}
	if err = manager.Close(); err != nil {
		t.Fatal(err)
	}
	client.CloseIdleConnections()
	connection, dialErr := net.DialTimeout("tcp", address, time.Second)
	if dialErr == nil {
		connection.Close()
		t.Fatal("automatic challenge listener survived Close")
	}
	if fixture.obtainCalls.Load() != 0 {
		t.Fatal("offline fixture attempted issuance")
	}
}
