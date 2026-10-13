package core_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
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

	core "github.com/xs23933/core/v3"
)

type rootBootstrapAuthority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
	pin         string
}

func rootBootstrapTestAuthority(t *testing.T) rootBootstrapAuthority {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bootstrap test root"}, IsCA: true, BasicConstraintsValid: true, MaxPathLen: 1, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	certificate, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(der)
	return rootBootstrapAuthority{certificate: certificate, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pin: hex.EncodeToString(digest[:])}
}
func rootBootstrapTestServerCertificate(t *testing.T, authority rootBootstrapAuthority, hostnameOK bool) tls.Certificate {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "bootstrap HTTPS endpoint"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if hostnameOK {
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		template.DNSNames = []string{"wrong.internal.test"}
	}
	der, e := x509.CreateCertificate(rand.Reader, template, authority.certificate, &key.PublicKey, authority.key)
	if e != nil {
		t.Fatal(e)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestRootBootstrapRejectsUntrustedEndpoint 从公开入口验证失败时不会发送引导凭据或申请证书。
func TestRootBootstrapRejectsUntrustedEndpoint(t *testing.T) {
	for _, scenario := range []string{"wrong-fingerprint", "wrong-hostname", "untrusted-https-chain", "redirect", "correct-pin"} {
		t.Run(scenario, func(t *testing.T) {
			authority := rootBootstrapTestAuthority(t)
			httpsAuthority := authority
			if scenario == "untrusted-https-chain" {
				httpsAuthority = rootBootstrapTestAuthority(t)
			}
			var rootCalls, enrollCalls, secretLeaks, redirectCalls atomic.Int32
			token := "unique-private-bootstrap-token-0123456789abcdef"
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectCalls.Add(1); w.WriteHeader(200) }))
			defer target.Close()
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/root.pem":
					rootCalls.Add(1)
					if r.Header.Get("Authorization") != "" || strings.Contains(r.URL.String(), token) {
						secretLeaks.Add(1)
					}
					if scenario == "redirect" {
						http.Redirect(w, r, target.URL+"/root.pem", http.StatusTemporaryRedirect)
						return
					}
					w.Header().Set("Content-Type", "application/x-pem-file")
					_, _ = w.Write(authority.pem)
				case "/enroll":
					enrollCalls.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+token {
						secretLeaks.Add(1)
					}
					http.Error(w, "test enrollment stops after verified bootstrap", http.StatusForbidden)
				default:
					http.NotFound(w, r)
				}
			}))
			server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{rootBootstrapTestServerCertificate(t, httpsAuthority, scenario != "wrong-hostname")}}
			server.StartTLS()
			defer server.Close()
			base := t.TempDir()
			tokenFile := filepath.Join(base, "token")
			if e := os.WriteFile(tokenFile, []byte(token), 0600); e != nil {
				t.Fatal(e)
			}
			pin := authority.pin
			if scenario == "wrong-fingerprint" {
				pin = strings.Repeat("0", 64)
			}
			state := filepath.Join(base, "state")
			app := core.New(core.Options{"debug": false, "workload": core.Options{"enabled": true, "service_name": "backend", "trust_domain": "integration", "issuer_url": server.URL, "root_sha256": pin, "bootstrap_token_file": tokenFile, "state_dir": state}})
			defer app.ShutdownWithTimeout(time.Second)
			err := app.EnableWorkloadIdentity()
			if err == nil {
				t.Fatal("测试签发器拒绝申请，初始化不应成功")
			}
			if strings.Contains(err.Error(), token) {
				t.Fatal("初始化错误泄露引导凭据")
			}
			if secretLeaks.Load() != 0 {
				t.Fatal("公共根下载携带了引导凭据，或申请未正确使用令牌")
			}
			if redirectCalls.Load() != 0 {
				t.Fatal("根引导请求跟随了重定向")
			}
			if rootCalls.Load() != 1 {
				t.Fatalf("根下载请求数=%d，期望1", rootCalls.Load())
			}
			expectedEnroll := int32(0)
			if scenario == "correct-pin" {
				expectedEnroll = 1
			}
			if enrollCalls.Load() != expectedEnroll {
				t.Fatalf("证书申请次数=%d，期望%d", enrollCalls.Load(), expectedEnroll)
			}
			cached, readError := os.ReadFile(filepath.Join(state, "root.pem"))
			if scenario == "correct-pin" {
				if readError != nil || string(cached) != string(authority.pem) {
					t.Fatalf("已验证根未缓存: %v", readError)
				}
			} else if readError == nil {
				t.Fatal("不可信根或HTTPS端点被写入缓存")
			}
		})
	}
}

// TestRootBootstrapRequiresTrustInput 验证自动下载仍要求独立部署信任，不能无认证接纳根。
func TestRootBootstrapRequiresTrustInput(t *testing.T) {
	for _, pin := range []string{"", "invalid", strings.Repeat("0", 63)} {
		t.Run(fmt.Sprintf("pin-length-%d", len(pin)), func(t *testing.T) {
			app := core.New(core.Options{"debug": false, "workload": core.Options{"enabled": true, "service_name": "backend", "trust_domain": "integration", "issuer_url": "https://127.0.0.1:1", "root_sha256": pin, "state_dir": filepath.Join(t.TempDir(), "state")}})
			defer app.ShutdownWithTimeout(time.Second)
			if e := app.EnableWorkloadIdentity(); e == nil {
				t.Fatal("缺少有效根指纹时不应自动建立信任")
			}
		})
	}
}
