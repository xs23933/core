package workload

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// IssuerConfig 配置单活签发器；PinFile 必须由独立于状态卷的部署存储保留。
type IssuerConfig struct {
	StateDir, TrustDomain, ListenAddress, AdvertiseURL, PolicyFile, PinFile string
	LeafTTL                                                                 time.Duration
}

// EnrollmentBinding 仅保存高熵引导令牌的 SHA-256 摘要。
type EnrollmentBinding struct {
	TokenSHA256 string `json:"token_sha256"`
}

// EnrollmentPolicy 将部署授予的引导资格绑定到逻辑服务。
type EnrollmentPolicy struct {
	Services map[string]EnrollmentBinding `json:"services"`
}

// Issuer 持有签发状态独占锁，签发与审计事实使用同一原子提交。
type Issuer struct {
	mu           sync.Mutex
	config       IssuerConfig
	state        issuerState
	lock         *os.File
	intermediate *x509.Certificate
	key          *ecdsa.PrivateKey
	server       *http.Server
	listener     net.Listener
	closed       bool
	doneCh       chan struct{}
	idempotent   atomic.Uint64
	rejected     atomic.Uint64
}
type issuerRequest struct {
	Service   string `json:"service"`
	CSRPEM    string `json:"csr_pem"`
	RequestID string `json:"request_id"`
}
type issuerResponse struct {
	CertificatePEM string `json:"certificate_pem"`
	CAPEM          string `json:"ca_pem"`
}

// OpenIssuer 首次创建 PKI；已提交状态损坏或指纹不符时拒绝重新生成信任根。
func OpenIssuer(c IssuerConfig) (*Issuer, error) {
	if c.StateDir == "" || !issuerDomain.MatchString(c.TrustDomain) || c.PolicyFile == "" || c.PinFile == "" {
		return nil, fmt.Errorf("issuer state, trust domain, policy and independent pin file are required")
	}
	stateAbs, pathError := filepath.Abs(c.StateDir)
	if pathError != nil {
		return nil, pathError
	}
	pinAbs, pathError := filepath.Abs(c.PinFile)
	if pathError != nil {
		return nil, pathError
	}
	relative, pathError := filepath.Rel(stateAbs, pinAbs)
	if pathError != nil {
		return nil, pathError
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("issuer pin file must be outside the state directory")
	}
	if c.LeafTTL == 0 {
		c.LeafTTL = 7 * 24 * time.Hour
	}
	if c.LeafTTL < time.Minute || c.LeafTTL > 30*24*time.Hour {
		return nil, fmt.Errorf("issuer leaf TTL must be between one minute and 30 days")
	}
	if c.ListenAddress == "" {
		return nil, fmt.Errorf("issuer listen address required")
	}
	lock, e := issuerLock(c.StateDir)
	if e != nil {
		return nil, e
	}
	i := &Issuer{config: c, lock: lock, doneCh: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			issuerUnlock(lock)
		}
	}()
	path := filepath.Join(c.StateDir, "issuer.json")
	b, e := issuerSecureRead(path)
	if errors.Is(e, os.ErrNotExist) {
		if _, pe := os.Lstat(c.PinFile); !errors.Is(pe, os.ErrNotExist) {
			return nil, fmt.Errorf("issuer state missing with existing pin; restore backup")
		}
		entries, re := os.ReadDir(c.StateDir)
		if re != nil {
			return nil, re
		}
		for _, entry := range entries {
			if entry.Name() != "issuer.lock" && !strings.HasPrefix(entry.Name(), ".issuer-pending-") {
				return nil, fmt.Errorf("uncommitted issuer material exists; explicit recovery required")
			}
		}
		var rootKey []byte
		i.state, rootKey, e = issuerCreatePKI(c)
		if e != nil {
			return nil, e
		}
		fp, fe := issuerFingerprint([]byte(i.state.RootPEM))
		if fe != nil {
			return nil, fe
		}
		// 先固定外部指纹，崩溃时宁可要求恢复，也不能静默更换已建立的信任根。
		if e = issuerCreatePin(c.PinFile, []byte(fp+"\n")); e != nil {
			return nil, e
		}
		if e = issuerAtomicWrite(filepath.Join(c.StateDir, "root-key.pem"), rootKey); e != nil {
			return nil, e
		}
		if e = issuerSave(path, i.state); e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	} else if e = json.Unmarshal(b, &i.state); e != nil {
		return nil, fmt.Errorf("invalid issuer state: %w", e)
	}
	if e = i.validate(); e != nil {
		return nil, e
	}
	if _, e = i.loadPolicy(); e != nil {
		return nil, e
	}
	if e = issuerAtomicWrite(filepath.Join(c.StateDir, "root.pem"), i.RootPEM()); e != nil {
		return nil, e
	}
	ok = true
	return i, nil
}
func (i *Issuer) validate() error {
	s := i.state
	if s.Version != 1 || s.TrustDomain != i.config.TrustDomain || s.AdvertiseURL != i.config.AdvertiseURL || s.Records == nil {
		return fmt.Errorf("issuer state configuration mismatch")
	}
	root, e := issuerParseCert([]byte(s.RootPEM))
	if e != nil {
		return e
	}
	fp, e := issuerFingerprint([]byte(s.RootPEM))
	if e != nil {
		return e
	}
	pin, e := issuerSecureRead(i.config.PinFile)
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(pin)) != fp {
		return fmt.Errorf("issuer root fingerprint mismatch")
	}
	if !root.IsCA || !root.BasicConstraintsValid || root.MaxPathLen != 1 || root.CheckSignatureFrom(root) != nil {
		return fmt.Errorf("invalid issuer root constraints")
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	inter, e := issuerParseCert([]byte(s.IntermediatePEM))
	if e != nil {
		return e
	}
	if !inter.IsCA || !inter.MaxPathLenZero || inter.MaxPathLen != 0 {
		return fmt.Errorf("invalid intermediate constraints")
	}
	if _, e = inter.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); e != nil {
		return e
	}
	key, e := issuerParseKey([]byte(s.IntermediateKeyPEM))
	if e != nil {
		return e
	}
	if !key.PublicKey.Equal(inter.PublicKey) {
		return fmt.Errorf("intermediate key mismatch")
	}
	i.intermediate = inter
	i.key = key
	if e = i.refreshHTTPSLocked(); e != nil {
		return e
	}
	s = i.state
	pair, e := tls.X509KeyPair([]byte(s.HTTPSPEM), []byte(s.HTTPSKeyPEM))
	if e != nil {
		return e
	}
	leaf, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil {
		return e
	}
	pool := x509.NewCertPool()
	pool.AddCert(inter)
	for _, der := range pair.Certificate[1:] {
		embedded, parseError := x509.ParseCertificate(der)
		if parseError != nil {
			return parseError
		}
		pool.AddCert(embedded)
	}
	u, e := url.Parse(i.config.AdvertiseURL)
	if e != nil {
		return e
	}
	_, e = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: pool, DNSName: u.Hostname(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return e
}
func (i *Issuer) loadPolicy() (EnrollmentPolicy, error) {
	var p EnrollmentPolicy
	b, e := issuerSecureRead(i.config.PolicyFile)
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal(b, &p); e != nil {
		return p, e
	}
	if len(p.Services) == 0 {
		return p, fmt.Errorf("empty enrollment policy")
	}
	seen := make(map[string]bool)
	for service, binding := range p.Services {
		v, e := hex.DecodeString(binding.TokenSHA256)
		if !issuerServiceName.MatchString(service) || e != nil || len(v) != sha256.Size || seen[strings.ToLower(binding.TokenSHA256)] {
			return p, fmt.Errorf("invalid enrollment policy binding")
		}
		seen[strings.ToLower(binding.TokenSHA256)] = true
	}
	return p, nil
}

// RootPEM 返回可通过可信部署渠道分发的公共根证书副本。
func (i *Issuer) RootPEM() []byte { return []byte(i.state.RootPEM) }

// IssueLocal 只供同进程可信调用方申请身份，不暴露网络绕过认证入口。
func (i *Issuer) IssueLocal(service string, csrPEM []byte, key string) ([]byte, error) {
	return i.issue(service, csrPEM, key, "local")
}
func (i *Issuer) issue(service string, csrPEM []byte, id, kind string) (certificate []byte, resultError error) {
	defer func() {
		if resultError != nil {
			i.rejected.Add(1)
		}
	}()
	if !issuerServiceName.MatchString(service) || id == "" || len(id) > 128 || len(csrPEM) > 64*1024 {
		return nil, fmt.Errorf("invalid issuance request")
	}
	block, rest := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, fmt.Errorf("invalid CSR PEM")
	}
	csr, e := x509.ParseCertificateRequest(block.Bytes)
	if e != nil {
		return nil, e
	}
	if e = csr.CheckSignature(); e != nil {
		return nil, e
	}
	// 签发身份完全来自授权绑定，不接受申请方自带的 SAN 或 CA 扩展。
	if len(csr.Extensions) > 0 || len(csr.ExtraExtensions) > 0 || len(csr.DNSNames) > 0 || len(csr.IPAddresses) > 0 || len(csr.URIs) > 0 || len(csr.EmailAddresses) > 0 {
		return nil, fmt.Errorf("CSR extensions are not permitted")
	}
	switch k := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if k.Curve.Params().BitSize < 256 {
			return nil, fmt.Errorf("weak CSR key")
		}
	default:
		return nil, fmt.Errorf("CSR requires ECDSA key")
	}
	digest := sha256.Sum256(csr.Raw)
	hash := hex.EncodeToString(digest[:])
	recordKey := id
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil, fmt.Errorf("issuer closed")
	}
	if r, ok := i.state.Records[recordKey]; ok {
		if r.Service != service || r.CSRDigest != hash {
			return nil, fmt.Errorf("issuance idempotency conflict")
		}
		i.idempotent.Add(1)
		return []byte(r.CertificatePEM), nil
	}
	now := time.Now()
	expiry := now.Add(i.config.LeafTTL)
	if expiry.After(i.intermediate.NotAfter.Add(-time.Minute)) {
		return nil, fmt.Errorf("issuer CA lifetime insufficient; controlled intermediate renewal required")
	}
	serial, e := issuerSerial()
	if e != nil {
		return nil, e
	}
	uri := &url.URL{Scheme: "spiffe", Host: i.config.TrustDomain, Path: "/" + service}
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: service}, NotBefore: now.Add(-time.Minute), NotAfter: expiry, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{uri}}
	leaf, e := issuerCert(t, i.intermediate, csr.PublicKey, i.key)
	if e != nil {
		return nil, e
	}
	chain := append(leaf, []byte(i.state.IntermediatePEM)...)
	i.state.Records[recordKey] = issuerRecord{Service: service, CSRDigest: hash, CertificatePEM: string(chain), IssuedAt: now.UTC().Format(time.RFC3339Nano), Serial: serial.String(), Kind: kind}
	if e = issuerSave(filepath.Join(i.config.StateDir, "issuer.json"), i.state); e != nil {
		delete(i.state.Records, recordKey)
		return nil, e
	}
	return chain, nil
}

// Start 绑定独立 HTTPS 入口后返回，后续由 Close 排空并释放状态锁。
func (i *Issuer) Start(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.server != nil {
		return fmt.Errorf("issuer already started or closed")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(i.RootPEM())
	mux := http.NewServeMux()
	mux.HandleFunc("/enroll", i.handleEnroll)
	mux.HandleFunc("/renew", i.handleRenew)
	listener, e := net.Listen("tcp", i.config.ListenAddress)
	if e != nil {
		return e
	}
	i.listener = listener
	i.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: i.currentHTTPSCertificate, ClientCAs: roots, ClientAuth: tls.VerifyClientCertIfGiven}}
	server := i.server
	go func() { _ = server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-i.doneCh:
		}
	}()
	return nil
}
func (i *Issuer) readRequest(w http.ResponseWriter, r *http.Request) (issuerRequest, bool) {
	var req issuerRequest
	if r.Method != http.MethodPost {
		i.reject(w, "method not allowed", 405)
		return req, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 96*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&req); e != nil {
		i.reject(w, "invalid request", 400)
		return req, false
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		i.reject(w, "invalid request", 400)
		return req, false
	}
	return req, true
}
func (i *Issuer) handleEnroll(w http.ResponseWriter, r *http.Request) {
	req, ok := i.readRequest(w, r)
	if !ok {
		return
	}
	policy, e := i.loadPolicy()
	if e != nil {
		i.reject(w, "enrollment policy unavailable", 503)
		return
	}
	binding, ok := policy.Services[req.Service]
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	hash := sha256.Sum256([]byte(token))
	expected, e := hex.DecodeString(binding.TokenSHA256)
	if !ok || e != nil || token == auth || len(token) < 32 || subtle.ConstantTimeCompare(hash[:], expected) != 1 {
		i.reject(w, "enrollment denied", 403)
		return
	}
	i.respond(w, req, "enroll")
}
func (i *Issuer) handleRenew(w http.ResponseWriter, r *http.Request) {
	req, ok := i.readRequest(w, r)
	if !ok {
		return
	}
	policy, e := i.loadPolicy()
	if e != nil {
		i.reject(w, "enrollment policy unavailable", 503)
		return
	}
	if _, ok = policy.Services[req.Service]; !ok {
		i.reject(w, "renewal denied", 403)
		return
	}
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		i.reject(w, "verified client identity required", 403)
		return
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(i.RootPEM())
	raw := make([][]byte, len(r.TLS.PeerCertificates))
	for n, cert := range r.TLS.PeerCertificates {
		raw[n] = cert.Raw
	}
	if e = verifyWorkload(raw, roots, i.config.TrustDomain, req.Service, x509.ExtKeyUsageClientAuth, time.Now()); e != nil {
		i.reject(w, "renewal identity mismatch", 403)
		return
	}
	i.respond(w, req, "renew")
}
func (i *Issuer) respond(w http.ResponseWriter, req issuerRequest, kind string) {
	cert, e := i.issue(req.Service, []byte(req.CSRPEM), req.RequestID, kind)
	if e != nil {
		http.Error(w, "certificate request rejected", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(issuerResponse{CertificatePEM: string(cert), CAPEM: string(i.RootPEM())})
}

// Close 先排空在途 HTTP 请求，再释放单活签发锁。
func (i *Issuer) Close(ctx context.Context) error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	server := i.server
	close(i.doneCh)
	i.mu.Unlock()
	var shutdownError error
	if server != nil {
		shutdownError = server.Shutdown(ctx)
		if shutdownError != nil {
			shutdownError = errors.Join(shutdownError, server.Close())
		}
	}
	return errors.Join(shutdownError, issuerUnlock(i.lock))
}

// refreshHTTPSLocked 在本地刷新入口证书，不通过网络向自身申请。
func (i *Issuer) refreshHTTPSLocked() error {
	leaf, e := issuerParseCert([]byte(i.state.HTTPSPEM))
	if e != nil {
		return e
	}
	if leaf.NotAfter.After(time.Now().Add(30 * 24 * time.Hour)) {
		return nil
	}
	now := time.Now()
	expiry := now.AddDate(1, 0, 0)
	if expiry.After(i.intermediate.NotAfter.Add(-time.Minute)) {
		expiry = i.intermediate.NotAfter.Add(-time.Minute)
	}
	if !expiry.After(now.Add(24 * time.Hour)) {
		return fmt.Errorf("issuer CA lifetime insufficient for HTTPS renewal")
	}
	key, e := issuerKey()
	if e != nil {
		return e
	}
	serial, e := issuerSerial()
	if e != nil {
		return e
	}
	u, e := url.Parse(i.config.AdvertiseURL)
	if e != nil {
		return e
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "workload enrollment"}, NotBefore: now.Add(-time.Minute), NotAfter: expiry, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{u.Hostname()}
	}
	cert, e := issuerCert(template, i.intermediate, &key.PublicKey, i.key)
	if e != nil {
		return e
	}
	private, e := issuerKeyPEM(key)
	if e != nil {
		return e
	}
	oldCert, oldKey := i.state.HTTPSPEM, i.state.HTTPSKeyPEM
	i.state.HTTPSPEM = string(append(cert, []byte(i.state.IntermediatePEM)...))
	i.state.HTTPSKeyPEM = string(private)
	if e = issuerSave(filepath.Join(i.config.StateDir, "issuer.json"), i.state); e != nil {
		i.state.HTTPSPEM = oldCert
		i.state.HTTPSKeyPEM = oldKey
		return e
	}
	return nil
}
func (i *Issuer) currentHTTPSCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil, fmt.Errorf("issuer closed")
	}
	if e := i.refreshHTTPSLocked(); e != nil {
		return nil, e
	}
	pair, e := tls.X509KeyPair([]byte(i.state.HTTPSPEM), []byte(i.state.HTTPSKeyPEM))
	if e != nil {
		return nil, e
	}
	return &pair, nil
}

// IssuerStats 仅暴露签发数量，不包含认证凭据或证书内容。
type IssuerStats struct{ Issued, Idempotent, Rejected uint64 }

// Stats 返回当前持久化签发量与本次进程运行中的重放、拒绝数量。
func (i *Issuer) Stats() IssuerStats {
	i.mu.Lock()
	defer i.mu.Unlock()
	return IssuerStats{Issued: uint64(len(i.state.Records)), Idempotent: i.idempotent.Load(), Rejected: i.rejected.Load()}
}
func (i *Issuer) reject(w http.ResponseWriter, message string, status int) {
	i.rejected.Add(1)
	http.Error(w, message, status)
}

// RotateIntermediate 执行受控中间 CA 更新，根私钥离线时明确拒绝。
// 已签发证书携带旧中间链，仍可通过同一个根证书验证。
func (i *Issuer) RotateIntermediate() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return fmt.Errorf("issuer closed")
	}
	root, e := issuerParseCert([]byte(i.state.RootPEM))
	if e != nil {
		return e
	}
	rootBytes, e := issuerSecureRead(filepath.Join(i.config.StateDir, "root-key.pem"))
	if e != nil {
		return fmt.Errorf("controlled intermediate rotation requires available offline root key: %w", e)
	}
	rootKey, e := issuerParseKey(rootBytes)
	if e != nil {
		return e
	}
	if !rootKey.PublicKey.Equal(root.PublicKey) {
		return fmt.Errorf("root key mismatch")
	}
	now := time.Now()
	expiry := now.AddDate(2, 0, 0)
	if expiry.After(root.NotAfter.Add(-time.Minute)) {
		expiry = root.NotAfter.Add(-time.Minute)
	}
	if !expiry.After(now.Add(30 * 24 * time.Hour)) {
		return fmt.Errorf("root lifetime insufficient for intermediate rotation")
	}
	key, e := issuerKey()
	if e != nil {
		return e
	}
	serial, e := issuerSerial()
	if e != nil {
		return e
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: i.config.TrustDomain + " workload issuer"}, NotBefore: now.Add(-time.Minute), NotAfter: expiry, IsCA: true, BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	cert, e := issuerCert(template, root, &key.PublicKey, rootKey)
	if e != nil {
		return e
	}
	private, e := issuerKeyPEM(key)
	if e != nil {
		return e
	}
	oldCert, oldKey := i.state.IntermediatePEM, i.state.IntermediateKeyPEM
	i.state.IntermediatePEM = string(cert)
	i.state.IntermediateKeyPEM = string(private)
	if e = issuerSave(filepath.Join(i.config.StateDir, "issuer.json"), i.state); e != nil {
		i.state.IntermediatePEM = oldCert
		i.state.IntermediateKeyPEM = oldKey
		return e
	}
	parsed, e := issuerParseCert(cert)
	if e != nil {
		return e
	}
	i.intermediate = parsed
	i.key = key
	return nil
}
