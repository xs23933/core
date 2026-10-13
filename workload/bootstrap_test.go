package workload

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func bootstrapTestServer(t *testing.T, ca *identityTestCA, host string, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(42), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, ClientAuth: tls.RequestClientCert}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}
func bootstrapTestConfig(t *testing.T, ca *identityTestCA, url string) Config {
	t.Helper()
	sum := sha256.Sum256(ca.cert.Raw)
	return Config{StateDir: t.TempDir(), IssuerURL: url, RootSHA256: hex.EncodeToString(sum[:])}
}
func TestPinnedRootDownloadAndOfflineCache(t *testing.T) {
	ca := newIdentityTestCA(t)
	var calls atomic.Int32
	server := bootstrapTestServer(t, ca, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/root.pem" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || len(r.TLS.PeerCertificates) != 0 {
			t.Error("unexpected root request secrets or path")
		}
		_, _ = w.Write(ca.pem)
	}))
	conf := bootstrapTestConfig(t, ca, server.URL)
	conf.RootSHA256 = "  " + strings.ToUpper(conf.RootSHA256) + "  "
	root, err := loadTrustBundle(context.Background(), conf, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(root) != string(ca.pem) {
		t.Fatal("root mismatch")
	}
	info, err := os.Stat(filepath.Join(conf.StateDir, "root.pem"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("cache mode err=%v", err)
	}
	server.Close()
	if _, err = loadTrustBundle(context.Background(), conf, true); err != nil {
		t.Fatalf("offline cache failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("cache triggered redundant download")
	}
}
func TestPinnedRootRejectsFailuresWithoutCaching(t *testing.T) {
	for _, scenario := range []string{"wrong-pin", "wrong-host", "wrong-chain", "redirect", "oversized", "not-ca"} {
		t.Run(scenario, func(t *testing.T) {
			ca := newIdentityTestCA(t)
			servingCA := ca
			host := "127.0.0.1"
			if scenario == "wrong-chain" {
				servingCA = newIdentityTestCA(t)
			}
			if scenario == "wrong-host" {
				host = "wrong.test"
			}
			payload := ca.pem
			if scenario == "not-ca" {
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				template := &x509.Certificate{SerialNumber: big.NewInt(12), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
				der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
				if err != nil {
					t.Fatal(err)
				}
				payload = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			}
			server := bootstrapTestServer(t, servingCA, host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || len(r.TLS.PeerCertificates) != 0 {
					t.Error("root download leaked credentials")
				}
				if scenario == "redirect" {
					http.Redirect(w, r, "/elsewhere", 302)
					return
				}
				if scenario == "oversized" {
					_, _ = w.Write([]byte(strings.Repeat("x", rootBundleLimit+1)))
					return
				}
				_, _ = w.Write(payload)
			}))
			conf := bootstrapTestConfig(t, ca, server.URL)
			if scenario == "wrong-pin" {
				conf.RootSHA256 = strings.Repeat("0", 64)
			}
			if scenario == "not-ca" {
				block, _ := pem.Decode(payload)
				sum := sha256.Sum256(block.Bytes)
				conf.RootSHA256 = hex.EncodeToString(sum[:])
			}
			if _, err := loadTrustBundle(context.Background(), conf, true); err == nil {
				t.Fatal("invalid bootstrap accepted")
			}
			if _, err := os.Stat(filepath.Join(conf.StateDir, "root.pem")); !os.IsNotExist(err) {
				t.Fatal("invalid root cached")
			}
		})
	}
}
func TestPinnedRootCacheErrorsDoNotDownload(t *testing.T) {
	ca := newIdentityTestCA(t)
	var calls atomic.Int32
	server := bootstrapTestServer(t, ca, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write(ca.pem) }))
	for _, scenario := range []string{"wrong-pin", "corrupt", "symlink", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			conf := bootstrapTestConfig(t, ca, server.URL)
			path := filepath.Join(conf.StateDir, "root.pem")
			switch scenario {
			case "wrong-pin":
				if err := os.WriteFile(path, newIdentityTestCA(t).pem, 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(conf.StateDir, "other.pem")
				if err := os.WriteFile(target, ca.pem, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadTrustBundle(context.Background(), conf, true); err == nil {
				t.Fatal("invalid cache accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("cache error triggered network fallback")
	}
}
func TestPinnedRootCancelledDownloadAndWriteFailure(t *testing.T) {
	ca := newIdentityTestCA(t)
	server := bootstrapTestServer(t, ca, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(ca.pem) }))
	conf := bootstrapTestConfig(t, ca, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loadTrustBundle(ctx, conf, true); err == nil {
		t.Fatal("cancelled download accepted")
	}
	conf.TrustBundleFile = filepath.Join(conf.StateDir, "missing-parent", "root.pem")
	if _, err := loadTrustBundle(context.Background(), conf, true); err == nil {
		t.Fatal("cache write failure ignored")
	}
	if _, err := os.Stat(conf.TrustBundleFile); !os.IsNotExist(err) {
		t.Fatal("write failure created cache")
	}
}

func TestPinnedRootRejectsEmptyQueryAndMissingTrustConfiguration(t *testing.T) {
	ca := newIdentityTestCA(t)
	var calls atomic.Int32
	server := bootstrapTestServer(t, ca, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write(ca.pem) }))
	conf := bootstrapTestConfig(t, ca, server.URL+"?")
	if _, err := loadTrustBundle(context.Background(), conf, true); err == nil {
		t.Fatal("empty query marker accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid URL caused request")
	}
	missingTrustDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(missingTrustDir, "root.pem"), ca.pem, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTrustBundle(context.Background(), Config{StateDir: missingTrustDir}, true); err == nil || !strings.Contains(err.Error(), "trust_bundle_file or root_sha256") {
		t.Fatalf("missing trust error=%v", err)
	}
}

func TestPinnedRootCancellationInterruptsDownload(t *testing.T) {
	ca := newIdentityTestCA(t)
	started := make(chan struct{})
	server := bootstrapTestServer(t, ca, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	conf := bootstrapTestConfig(t, ca, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := loadTrustBundle(ctx, conf, true); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("download did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled download succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt root download")
	}
	if _, err := os.Stat(filepath.Join(conf.StateDir, "root.pem")); !os.IsNotExist(err) {
		t.Fatal("cancelled root cached")
	}
}
