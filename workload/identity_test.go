package workload

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type identityTestCA struct {
	key   *ecdsa.PrivateKey
	cert  *x509.Certificate
	pem   []byte
	mu    sync.Mutex
	count int
	ttl   time.Duration
}

func newIdentityTestCA(t *testing.T) *identityTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &identityTestCA{key: key, cert: cert, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ttl: time.Minute}
}
func (c *identityTestCA) sign(service string, csrPEM []byte, _ string) ([]byte, []byte, error) {
	block, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if err = csr.CheckSignature(); err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	c.count++
	serial := c.count
	ttl := c.ttl
	c.mu.Unlock()
	uri, _ := url.Parse("spiffe://test/" + service)
	template := &x509.Certificate{SerialNumber: big.NewInt(int64(serial + 1)), NotBefore: time.Now().Add(-time.Second), NotAfter: time.Now().Add(ttl), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, URIs: []*url.URL{uri}}
	der, err := x509.CreateCertificate(rand.Reader, template, c.cert, csr.PublicKey, c.key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), c.pem, err
}
func (c *identityTestCA) config(t *testing.T, service string) Config {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root.pem")
	if err := os.WriteFile(root, c.pem, 0600); err != nil {
		t.Fatal(err)
	}
	return Config{ServiceName: service, TrustDomain: "test", TrustBundleFile: root, StateDir: filepath.Join(dir, "identity"), RetryInterval: 10 * time.Millisecond}
}
func testIdentity(t *testing.T, ca *identityTestCA, service string) *Identity {
	t.Helper()
	id, err := openIdentity(context.Background(), ca.config(t, service), ca.sign)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := id.Close(); err != nil {
			t.Error(err)
		}
	})
	return id
}
func identityHandshake(server, client *tls.Config) (error, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err, err
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(time.Second))
		tc := tls.Server(conn, server)
		err = tc.Handshake()
		if err == nil {
			_, err = tc.Write([]byte{1})
		}
		serverResult <- err
	}()
	raw, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		return err, <-serverResult
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(time.Second))
	tc := tls.Client(raw, client)
	err = tc.Handshake()
	if err == nil {
		var b [1]byte
		_, err = tc.Read(b[:])
	}
	return err, <-serverResult
}

func TestIdentityTLSAndReload(t *testing.T) {
	ca := newIdentityTestCA(t)
	client := testIdentity(t, ca, "caller")
	server := testIdentity(t, ca, "backend")
	if ce, se := identityHandshake(server.ServerTLSConfig(), client.ClientTLSConfig("backend")); ce != nil || se != nil {
		t.Fatalf("valid TLS: client=%v server=%v", ce, se)
	}
	if ce, _ := identityHandshake(server.ServerTLSConfig(), client.ClientTLSConfig("wrong")); ce == nil {
		t.Fatal("wrong target accepted")
	}
	noCert := client.ClientTLSConfig("backend")
	noCert.GetClientCertificate = nil
	if ce, se := identityHandshake(server.ServerTLSConfig(), noCert); ce == nil && se == nil {
		t.Fatal("missing client certificate accepted")
	}
	stranger := testIdentity(t, newIdentityTestCA(t), "outsider")
	if ce, se := identityHandshake(server.ServerTLSConfig(), stranger.ClientTLSConfig("backend")); ce == nil && se == nil {
		t.Fatal("stranger CA accepted")
	}
	conf := ca.config(t, "reload")
	first, err := openIdentity(context.Background(), conf, ca.sign)
	if err != nil {
		t.Fatal(err)
	}
	serial := first.cert.Leaf.SerialNumber.String()
	if _, err := openIdentity(context.Background(), conf, ca.sign); err == nil {
		t.Fatal("shared state lock accepted")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := openIdentity(context.Background(), conf, ca.sign)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.cert.Leaf.SerialNumber.String() != serial {
		t.Fatal("restart reissued identity")
	}
}

func TestIdentityAutomaticRenewalAndExpiry(t *testing.T) {
	ca := newIdentityTestCA(t)
	ca.ttl = 2 * time.Second
	conf := ca.config(t, "backend")
	conf.RenewBefore = time.Second
	id, err := openIdentity(context.Background(), conf, ca.sign)
	if err != nil {
		t.Fatal(err)
	}
	defer id.Close()
	first := id.ExpiresAt()
	deadline := time.Now().Add(4 * time.Second)
	for !id.ExpiresAt().After(first) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !id.ExpiresAt().After(first) {
		t.Fatal("background renewal did not rotate")
	}
	client := testIdentity(t, ca, "caller")
	if ce, se := identityHandshake(id.ServerTLSConfig(), client.ClientTLSConfig("backend")); ce != nil || se != nil {
		t.Fatalf("rotated TLS: %v %v", ce, se)
	}
}

func TestIdentityRejectsCorruptCache(t *testing.T) {
	ca := newIdentityTestCA(t)
	conf := ca.config(t, "backend")
	if err := os.Mkdir(conf.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf.StateDir, "identity.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openIdentity(context.Background(), conf, ca.sign); err == nil {
		t.Fatal("corrupt state silently replaced")
	}
}

func TestIdentityExpiredCacheRecovery(t *testing.T) {
	ca := newIdentityTestCA(t)
	ca.ttl = time.Second
	conf := ca.config(t, "backend")
	first, err := openIdentity(context.Background(), conf, ca.sign)
	if err != nil {
		t.Fatal(err)
	}
	serial := first.cert.Leaf.SerialNumber.String()
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.Expired():
		t.Fatal("shutdown incorrectly signaled expiry")
	default:
	}
	time.Sleep(1200 * time.Millisecond)
	second, err := openIdentity(context.Background(), conf, ca.sign)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !second.Valid() || second.cert.Leaf.SerialNumber.String() == serial {
		t.Fatal("expired cache was not recovered")
	}
}

func TestIdentityExpiryNotification(t *testing.T) {
	ca := newIdentityTestCA(t)
	ca.ttl = 2 * time.Second
	conf := ca.config(t, "backend")
	conf.RenewBefore = time.Second
	var calls atomic.Int32
	signer := func(service string, csr []byte, key string) ([]byte, []byte, error) {
		if calls.Add(1) > 1 {
			return nil, nil, errors.New("issuer unavailable")
		}
		return ca.sign(service, csr, key)
	}
	id, err := openIdentity(context.Background(), conf, signer)
	if err != nil {
		t.Fatal(err)
	}
	defer id.Close()
	select {
	case <-id.Expired():
	case <-time.After(4 * time.Second):
		t.Fatal("expired identity not invalidated")
	}
	if id.Valid() {
		t.Fatal("expired identity remains valid")
	}
	if _, err = id.currentCertificate(); err == nil {
		t.Fatal("expired identity still available for handshake")
	}
}

func TestIdentityRetryPreservesCSRAndIdempotencyKey(t *testing.T) {
	ca := newIdentityTestCA(t)
	conf := ca.config(t, "backend")
	var csr []byte
	var request string
	failed := func(service string, pem []byte, key string) ([]byte, []byte, error) {
		csr = append([]byte(nil), pem...)
		request = key
		return nil, nil, errors.New("response lost")
	}
	if _, err := openIdentity(context.Background(), conf, failed); err == nil {
		t.Fatal("failed enrollment accepted")
	}
	retried := func(service string, pem []byte, key string) ([]byte, []byte, error) {
		if !bytes.Equal(csr, pem) || request != key {
			t.Error("restart changed pending CSR or request ID")
		}
		return ca.sign(service, pem, key)
	}
	id, err := openIdentity(context.Background(), conf, retried)
	if err != nil {
		t.Fatal(err)
	}
	defer id.Close()
}

func TestIdentityClosedCredentialsFail(t *testing.T) {
	ca := newIdentityTestCA(t)
	id := testIdentity(t, ca, "closed")
	config := id.ClientTLSConfig("backend")
	if err := id.Close(); err != nil {
		t.Fatal(err)
	}
	if id.Valid() {
		t.Fatal("closed identity remains valid")
	}
	if _, err := config.GetClientCertificate(nil); err == nil {
		t.Fatal("closed identity still supplies credentials")
	}
}

func TestIdentityExpiredIdempotentResponseRecovers(t *testing.T) {
	ca := newIdentityTestCA(t)
	ca.ttl = time.Second
	conf := ca.config(t, "backend")
	var cached []byte
	var originalID string
	lost := func(service string, csr []byte, key string) ([]byte, []byte, error) {
		var err error
		cached, _, err = ca.sign(service, csr, key)
		if err != nil {
			return nil, nil, err
		}
		originalID = key
		return nil, nil, errors.New("response lost")
	}
	if _, err := openIdentity(context.Background(), conf, lost); err == nil {
		t.Fatal("lost response accepted")
	}
	time.Sleep(1200 * time.Millisecond)
	retry := func(service string, csr []byte, key string) ([]byte, []byte, error) {
		if key == originalID {
			return cached, ca.pem, nil
		}
		return ca.sign(service, csr, key)
	}
	id, err := openIdentity(context.Background(), conf, retry)
	if err != nil {
		t.Fatal(err)
	}
	defer id.Close()
	if !id.Valid() {
		t.Fatal("expired idempotent response not replaced")
	}
}

func TestIdentityResponseCannotReplacePinnedRoots(t *testing.T) {
	trusted := newIdentityTestCA(t)
	stranger := newIdentityTestCA(t)
	conf := trusted.config(t, "backend")
	if _, err := openIdentity(context.Background(), conf, stranger.sign); err == nil {
		t.Fatal("issuer response replaced pinned roots")
	}
}

func TestIdentityStatsSnapshot(t *testing.T) {
	ca := newIdentityTestCA(t)
	id := testIdentity(t, ca, "backend")
	snapshot := id.Stats()
	if snapshot.Attempts != 1 || snapshot.Successes != 1 || snapshot.Failures != 0 || snapshot.Remaining <= 0 || snapshot.ExpiresAt.IsZero() {
		t.Fatalf("invalid stats snapshot: %+v", snapshot)
	}
}
