package workload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const rootBundleLimit = 64 << 10

func loadTrustBundle(ctx context.Context, conf Config, allowDownload bool) ([]byte, error) {
	pinText := strings.TrimSpace(conf.RootSHA256)
	if pinText == "" {
		if strings.TrimSpace(conf.TrustBundleFile) == "" {
			return nil, errors.New("workload requires trust_bundle_file or root_sha256")
		}
		return os.ReadFile(conf.TrustBundleFile)
	}
	pin, err := hex.DecodeString(pinText)
	if err != nil || len(pin) != sha256.Size {
		return nil, errors.New("workload root_sha256 must contain 64 hexadecimal characters")
	}
	path := conf.TrustBundleFile
	if path == "" {
		path = filepath.Join(conf.StateDir, "root.pem")
	}
	if err = rejectRootSymlinks(path); err != nil {
		return nil, err
	}
	data, err := readRootCache(path)
	if err == nil {
		if _, err = validatePinnedRoot(data, pin); err != nil {
			return nil, err
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("workload root cache unavailable: %w", err)
	}
	if !allowDownload {
		return nil, errors.New("workload local issuer requires existing trusted root")
	}
	data, err = downloadPinnedRoot(ctx, conf.IssuerURL, pin)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = saveRootCache(path, data); err != nil {
		return nil, fmt.Errorf("workload root cache write failed: %w", err)
	}
	return data, nil
}

// rejectRootSymlinks 禁止信任缓存及其目录通过符号链接重定向。
func rejectRootSymlinks(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// 只检查缓存及直接父目录；系统级 /var 等规范别名不属于缓存重定向。
	for _, current := range []string{absolute, filepath.Dir(absolute)} {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("workload root cache symlinks are forbidden")
		}

	}
	return nil
}

func readRootCache(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("workload root cache must be a regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("workload root cache must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, rootBundleLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > rootBundleLimit {
		return nil, errors.New("workload root cache exceeds size limit")
	}
	return data, nil
}

func validatePinnedRoot(data, pin []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("workload root must contain exactly one PEM certificate")
	}
	// pem.Decode 会忽略前置文本；信任材料必须以证书边界开始。
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "-----BEGIN CERTIFICATE-----") {
		return nil, errors.New("workload root contains unexpected leading data")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid workload root certificate")
	}
	digest := sha256.Sum256(cert.Raw)
	if subtle.ConstantTimeCompare(digest[:], pin) != 1 {
		return nil, errors.New("workload root fingerprint mismatch")
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 || !bytes.Equal(cert.RawIssuer, cert.RawSubject) || cert.CheckSignatureFrom(cert) != nil {
		return nil, errors.New("workload root must be a self-signed certificate authority")
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return nil, errors.New("workload root certificate is not currently valid")
	}
	return cert, nil
}

func downloadPinnedRoot(ctx context.Context, issuerURL string, pin []byte) ([]byte, error) {
	endpoint, err := url.Parse(issuerURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, errors.New("workload root issuer_url must be trusted HTTPS URL")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/root.pem"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13,
		// 仅下载无秘密的公共根时延后校验；响应返回前必须完成指纹及本次 HTTPS 链校验。
		InsecureSkipVerify: true,
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("workload root redirects forbidden") }}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("workload root download failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workload root download rejected: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, rootBundleLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > rootBundleLimit {
		return nil, errors.New("workload root response exceeds size limit")
	}
	root, err := validatePinnedRoot(data, pin)
	if err != nil {
		return nil, err
	}
	if response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		return nil, errors.New("workload root response has no TLS peer identity")
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	intermediates := x509.NewCertPool()
	for _, cert := range response.TLS.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	if _, err = response.TLS.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: endpoint.Hostname(), CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return nil, fmt.Errorf("workload root HTTPS identity verification failed: %w", err)
	}
	return data, nil
}

func saveRootCache(path string, data []byte) error {
	if err := rejectRootSymlinks(path); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	f, err := os.CreateTemp(parent, ".workload-root-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
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
	// 硬链接原子提交且不覆盖已有缓存，避免并发替换或覆盖异常文件。
	if err = os.Link(temporary, path); err != nil {
		return err
	}
	if err = os.Remove(temporary); err != nil {
		return err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
