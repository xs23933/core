package workload

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPinnedRootDownloadEnrollAndAutomaticMTLSRenewal(t *testing.T) {
	ca := newIdentityTestCA(t)
	ca.ttl = 3 * time.Second
	var roots, enrollments, renewals atomic.Int32
	renewed := make(chan struct{}, 1)
	token := "test-bootstrap-token-with-at-least-thirty-two-bytes"
	trust := x509.NewCertPool()
	trust.AddCert(ca.cert)
	server := bootstrapTestServer(t, ca, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/root.pem":
			roots.Add(1)
			if r.Header.Get("Authorization") != "" || len(r.TLS.PeerCertificates) != 0 {
				t.Error("public root request leaked credentials")
			}
			_, _ = w.Write(ca.pem)
			return
		case "/enroll":
			enrollments.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+token || len(r.TLS.PeerCertificates) != 0 {
				http.Error(w, "invalid bootstrap authentication", 403)
				return
			}
		case "/renew":
			renewals.Add(1)
			if r.Header.Get("Authorization") != "" {
				http.Error(w, "renewal leaked bootstrap token", 403)
				return
			}
			// HTTPS 已证明客户端私钥持有；再按正式 transport 合同核验链、用途和 URI。
			raw := make([][]byte, len(r.TLS.PeerCertificates))
			for index, cert := range r.TLS.PeerCertificates {
				raw[index] = cert.Raw
			}
			if err := verifyWorkload(raw, trust, "test", "backend", x509.ExtKeyUsageClientAuth, time.Now()); err != nil {
				t.Errorf("renewal mTLS identity: %v", err)
				http.Error(w, "invalid renewal identity", 403)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		var request struct {
			Service   string `json:"service"`
			CSRPEM    string `json:"csr_pem"`
			RequestID string `json:"request_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		if request.Service != "backend" || request.RequestID == "" {
			http.Error(w, "invalid issuance identity", 403)
			return
		}
		cert, _, err := ca.sign(request.Service, []byte(request.CSRPEM), request.RequestID)
		if err != nil {
			http.Error(w, "sign failed", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err = json.NewEncoder(w).Encode(map[string]string{"certificate_pem": string(cert), "ca_pem": string(ca.pem)}); err != nil {
			t.Errorf("certificate response: %v", err)
			return
		}
		if r.URL.Path == "/renew" {
			select {
			case renewed <- struct{}{}:
			default:
			}
		}
	}))
	conf := bootstrapTestConfig(t, ca, server.URL)
	conf.StateDir = filepath.Join(conf.StateDir, "identity")
	if err := os.Mkdir(conf.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	conf.ServiceName = "backend"
	conf.TrustDomain = "test"
	conf.RenewBefore = time.Second
	conf.RetryInterval = 20 * time.Millisecond
	conf.BootstrapTokenFile = filepath.Join(conf.StateDir, "token")
	if err := os.WriteFile(conf.BootstrapTokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := Open(context.Background(), conf)
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()
	originalExpiry := identity.ExpiresAt()
	select {
	case <-renewed:
	case <-time.After(6 * time.Second):
		t.Fatal("automatic mTLS renewal not observed")
	}
	// HTTP 响应已发出，等待后台客户端完成校验与本地原子提交。
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for !identity.ExpiresAt().After(originalExpiry) {
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("renewed certificate not activated")
		}
	}
	if !identity.Valid() {
		t.Fatal("renewed identity is not valid")
	}
	if roots.Load() != 1 || enrollments.Load() != 1 || renewals.Load() != 1 {
		t.Fatalf("unexpected requests: roots=%d enrollments=%d renewals=%d", roots.Load(), enrollments.Load(), renewals.Load())
	}
	if _, err = os.Stat(filepath.Join(conf.StateDir, "root.pem")); err != nil {
		t.Fatal("downloaded root not cached")
	}
}
