package workload

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func issuerTestConfig(t *testing.T) (IssuerConfig, string) {
	t.Helper()
	base := t.TempDir()
	token := "long-private-bootstrap-token-0123456789"
	hash := sha256.Sum256([]byte(token))
	policy := EnrollmentPolicy{Services: map[string]EnrollmentBinding{"ledger": {TokenSHA256: hex.EncodeToString(hash[:])}, "settlement": {TokenSHA256: fmt.Sprintf("%064x", 2)}}}
	b, e := json.Marshal(policy)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(base, "policy.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	return IssuerConfig{StateDir: filepath.Join(base, "state"), PinFile: filepath.Join(base, "root.pin"), PolicyFile: path, TrustDomain: "test", ListenAddress: "127.0.0.1:0", AdvertiseURL: "https://127.0.0.1:9444"}, token
}
func issuerTestCSR(t *testing.T, withSAN bool) ([]byte, []byte) {
	t.Helper()
	k, e := issuerKey()
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.CertificateRequest{}
	if withSAN {
		template.URIs = []*url.URL{{Scheme: "spiffe", Host: "test", Path: "/gateway"}}
	}
	der, e := x509.CreateCertificateRequest(rand.Reader, template, k)
	if e != nil {
		t.Fatal(e)
	}
	key, e := issuerKeyPEM(k)
	if e != nil {
		t.Fatal(e)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), key
}
func issuerTestOpen(t *testing.T, c IssuerConfig) *Issuer {
	t.Helper()
	i, e := OpenIssuer(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := i.Close(context.Background()); e != nil {
			t.Error(e)
		}
	})
	return i
}
func TestIssuerPersistenceIdempotencyAndLock(t *testing.T) {
	c, _ := issuerTestConfig(t)
	i := issuerTestOpen(t, c)
	root := i.RootPEM()
	if _, e := OpenIssuer(c); e == nil {
		t.Fatal("second process acquired issuer lock")
	}
	csr, _ := issuerTestCSR(t, false)
	cert, e := i.IssueLocal("ledger", csr, "operation-1")
	if e != nil {
		t.Fatal(e)
	}
	leaf, e := issuerParseCert(cert)
	if e != nil {
		t.Fatal(e)
	}
	if leaf.IsCA || len(leaf.URIs) != 1 || leaf.URIs[0].String() != "spiffe://test/ledger" || len(leaf.ExtKeyUsage) != 2 {
		t.Fatal("invalid leaf identity or constraints")
	}
	if _, e = i.IssueLocal("settlement", csr, "operation-1"); e == nil {
		t.Fatal("cross-service idempotency conflict accepted")
	}
	csr2, _ := issuerTestCSR(t, false)
	if _, e = i.IssueLocal("ledger", csr2, "operation-1"); e == nil {
		t.Fatal("different CSR accepted for idempotency key")
	}
	if e = i.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	i = issuerTestOpen(t, c)
	again, e := i.IssueLocal("ledger", csr, "operation-1")
	if e != nil || !bytes.Equal(cert, again) || !bytes.Equal(root, i.RootPEM()) {
		t.Fatalf("restart changed signed material: %v", e)
	}
	// 根私钥可离线保管，日常签发不加载它。
	if e = os.Remove(filepath.Join(c.StateDir, "root-key.pem")); e != nil {
		t.Fatal(e)
	}
	if e = i.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	issuerTestOpen(t, c)
}
func TestIssuerRejectsCorruptionAndLostVolume(t *testing.T) {
	for _, mode := range []string{"state-corrupt", "state-missing", "pin-mismatch", "intermediate-key", "https-expired-config"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := issuerTestConfig(t)
			i := issuerTestOpen(t, c)
			if e := i.Close(context.Background()); e != nil {
				t.Fatal(e)
			}
			state := filepath.Join(c.StateDir, "issuer.json")
			switch mode {
			case "state-corrupt":
				if e := os.WriteFile(state, []byte("bad"), 0600); e != nil {
					t.Fatal(e)
				}
			case "state-missing":
				if e := os.RemoveAll(c.StateDir); e != nil {
					t.Fatal(e)
				}
			case "pin-mismatch":
				if e := os.WriteFile(c.PinFile, []byte("wrong"), 0600); e != nil {
					t.Fatal(e)
				}
			case "intermediate-key":
				s := i.state
				s.IntermediateKeyPEM = "bad"
				if e := issuerSave(state, s); e != nil {
					t.Fatal(e)
				}
			case "https-expired-config":
				c.AdvertiseURL = "https://other-host:9444"
			}
			if next, e := OpenIssuer(c); e == nil {
				next.Close(context.Background())
				t.Fatal("unsafe PKI regeneration or corrupt material accepted")
			}
		})
	}
}
func TestIssuerCSRRejectionAndParallelIdempotency(t *testing.T) {
	c, _ := issuerTestConfig(t)
	i := issuerTestOpen(t, c)
	csr, _ := issuerTestCSR(t, true)
	if _, e := i.IssueLocal("ledger", csr, "san"); e == nil {
		t.Fatal("CSR SAN escalation accepted")
	}
	csr, _ = issuerTestCSR(t, false)
	if _, e := i.IssueLocal("../gateway", csr, "path"); e == nil {
		t.Fatal("invalid service accepted")
	}
	var wg sync.WaitGroup
	results := make(chan []byte, 20)
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, e := i.IssueLocal("ledger", csr, "parallel")
			if e != nil {
				t.Error(e)
				return
			}
			results <- b
		}()
	}
	wg.Wait()
	close(results)
	var first []byte
	for b := range results {
		if first == nil {
			first = b
		}
		if !bytes.Equal(first, b) {
			t.Error("parallel issuance returned different certificates")
		}
	}
	if len(i.state.Records) != 1 {
		t.Fatal("parallel issuance duplicated audit record")
	}
}
func TestIssuerHTTPSBootstrapAndMTLSRenew(t *testing.T) {
	c, token := issuerTestConfig(t)
	i := issuerTestOpen(t, c)
	if e := i.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(i.RootPEM())
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}}
	t.Cleanup(func() { client.CloseIdleConnections() })
	endpoint := "https://" + i.listener.Addr().String()
	csr, key := issuerTestCSR(t, false)
	request := func(client *http.Client, path, service, id, auth string) (int, issuerResponse) {
		t.Helper()
		body, e := json.Marshal(issuerRequest{Service: service, CSRPEM: string(csr), RequestID: id})
		if e != nil {
			t.Fatal(e)
		}
		r, e := http.NewRequest(http.MethodPost, endpoint+path, bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		response, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		var result issuerResponse
		if response.StatusCode == 200 {
			if e = json.NewDecoder(response.Body).Decode(&result); e != nil {
				t.Fatal(e)
			}
		} else {
			if _, e = io.Copy(io.Discard, response.Body); e != nil {
				t.Fatal(e)
			}
		}
		return response.StatusCode, result
	}
	if code, _ := request(client, "/enroll", "ledger", "denied", "bad"); code != 403 {
		t.Fatalf("bad token status %d", code)
	}
	if code, _ := request(client, "/enroll", "gateway", "forged", token); code != 403 {
		t.Fatalf("forged service status %d", code)
	}
	code, leaf := request(client, "/enroll", "ledger", "enroll", token)
	if code != 200 {
		t.Fatalf("enroll status %d", code)
	}
	if code, _ = request(client, "/renew", "ledger", "no-cert", ""); code != 403 {
		t.Fatalf("unauthenticated renewal %d", code)
	}
	pair, e := tls.X509KeyPair([]byte(leaf.CertificatePEM), key)
	if e != nil {
		t.Fatal(e)
	}
	mtls := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{pair}}}}
	defer mtls.CloseIdleConnections()
	if code, _ = request(mtls, "/renew", "settlement", "cross-service", ""); code != 403 {
		t.Fatalf("cross-service renewal %d", code)
	}
	if code, _ = request(mtls, "/renew", "ledger", "renew", ""); code != 200 {
		t.Fatalf("renew status %d", code)
	}
	// 已撤销服务不允许依赖旧证书继续续期。
	policy := EnrollmentPolicy{Services: map[string]EnrollmentBinding{"settlement": {TokenSHA256: fmt.Sprintf("%064x", 1)}}}
	b, e := json.Marshal(policy)
	if e != nil {
		t.Fatal(e)
	}
	if e = issuerAtomicWrite(c.PolicyFile, b); e != nil {
		t.Fatal(e)
	}
	if code, _ = request(mtls, "/renew", "ledger", "revoked", ""); code != 403 {
		t.Fatalf("revoked service renewal %d", code)
	}
}

func TestIssuerHTTPSLocalRotationAndWriteFailure(t *testing.T) {
	c, _ := issuerTestConfig(t)
	i := issuerTestOpen(t, c)
	old := i.state.HTTPSPEM
	parsed, e := issuerParseCert([]byte(old))
	if e != nil {
		t.Fatal(e)
	}
	key, e := issuerParseKey([]byte(i.state.HTTPSKeyPEM))
	if e != nil {
		t.Fatal(e)
	}
	parsed.NotAfter = time.Now().Add(time.Hour)
	replacement, e := issuerCert(parsed, i.intermediate, &key.PublicKey, i.key)
	if e != nil {
		t.Fatal(e)
	}
	i.state.HTTPSPEM = string(append(replacement, []byte(i.state.IntermediatePEM)...))
	if e = issuerSave(filepath.Join(c.StateDir, "issuer.json"), i.state); e != nil {
		t.Fatal(e)
	}
	if _, e = i.currentHTTPSCertificate(nil); e != nil {
		t.Fatal(e)
	}
	if i.state.HTTPSPEM == old || i.state.HTTPSPEM == string(replacement) {
		t.Fatal("HTTPS certificate did not rotate")
	}
	csr, _ := issuerTestCSR(t, false)
	original := i.config.StateDir
	i.config.StateDir = filepath.Join(original, "missing-directory")
	if _, e = i.IssueLocal("ledger", csr, "write-fail"); e == nil {
		t.Fatal("write failure returned successful issuance")
	}
	if len(i.state.Records) != 0 {
		t.Fatal("write failure activated nonpersistent issuance")
	}
	i.config.StateDir = original
	if _, e = i.IssueLocal("ledger", csr, "write-fail"); e != nil {
		t.Fatal(e)
	}
}
func TestIssuerIncompleteInitializationAndPermissions(t *testing.T) {
	t.Run("precommit-temporary-material", func(t *testing.T) {
		c, _ := issuerTestConfig(t)
		if e := os.MkdirAll(c.StateDir, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(c.StateDir, ".issuer-pending-crash"), []byte("uncommitted"), 0600); e != nil {
			t.Fatal(e)
		}
		issuerTestOpen(t, c)
	})
	t.Run("unsafe-state-permissions", func(t *testing.T) {
		c, _ := issuerTestConfig(t)
		if e := os.MkdirAll(c.StateDir, 0755); e != nil {
			t.Fatal(e)
		}
		if i, e := OpenIssuer(c); e == nil {
			i.Close(context.Background())
			t.Fatal("public state directory accepted")
		}
	})
	t.Run("committed-pin-without-state", func(t *testing.T) {
		c, _ := issuerTestConfig(t)
		if e := os.WriteFile(c.PinFile, []byte("existing-deployment"), 0600); e != nil {
			t.Fatal(e)
		}
		if i, e := OpenIssuer(c); e == nil {
			i.Close(context.Background())
			t.Fatal("lost state silently created new root")
		}
	})
}

func TestIssuerControlledIntermediateRotationAndStats(t *testing.T) {
	c, _ := issuerTestConfig(t)
	i := issuerTestOpen(t, c)
	csr, _ := issuerTestCSR(t, false)
	old, e := i.IssueLocal("ledger", csr, "before-rotation")
	if e != nil {
		t.Fatal(e)
	}
	if e = i.RotateIntermediate(); e != nil {
		t.Fatal(e)
	}
	newCert, e := i.IssueLocal("ledger", csr, "after-rotation")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(old, newCert) {
		t.Fatal("rotation reused leaf")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(i.RootPEM())
	for _, chain := range [][]byte{old, newCert} {
		var raw [][]byte
		for len(chain) > 0 {
			p, rest := pem.Decode(chain)
			if p == nil {
				break
			}
			raw = append(raw, p.Bytes)
			chain = rest
		}
		if e = verifyWorkload(raw, roots, "test", "ledger", x509.ExtKeyUsageClientAuth, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = i.IssueLocal("ledger", csr, "before-rotation"); e != nil {
		t.Fatal(e)
	}
	if _, e = i.IssueLocal("../invalid", csr, "bad"); e == nil {
		t.Fatal("invalid service accepted")
	}
	stats := i.Stats()
	if stats.Issued != 2 || stats.Idempotent != 1 || stats.Rejected != 1 {
		t.Fatalf("wrong issuer counters: %+v", stats)
	}
	if e = i.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	i = issuerTestOpen(t, c)
	if i.Stats().Issued != 2 {
		t.Fatal("persisted audit count lost")
	}
	if e = os.Remove(filepath.Join(c.StateDir, "root-key.pem")); e != nil {
		t.Fatal(e)
	}
	if e = i.RotateIntermediate(); e == nil {
		t.Fatal("offline root silently replaced")
	}
}

func TestIssuerIndependentPinExclusiveCreation(t *testing.T) {
	c, _ := issuerTestConfig(t)
	inside := c
	inside.PinFile = filepath.Join(c.StateDir, "root.pin")
	if i, e := OpenIssuer(inside); e == nil {
		i.Close(context.Background())
		t.Fatal("根指纹不能与签发状态共用持久目录")
	}
	second := c
	second.StateDir = c.StateDir + "-replica"
	var wg sync.WaitGroup
	success := make(chan *Issuer, 2)
	for _, config := range []IssuerConfig{c, second} {
		wg.Add(1)
		go func(config IssuerConfig) {
			defer wg.Done()
			i, e := OpenIssuer(config)
			if e == nil {
				success <- i
			}
		}(config)
	}
	wg.Wait()
	close(success)
	count := 0
	for i := range success {
		count++
		if e := i.Close(context.Background()); e != nil {
			t.Error(e)
		}
	}
	if count != 1 {
		t.Fatalf("同一部署指纹有%d个首次初始化成功", count)
	}
}
