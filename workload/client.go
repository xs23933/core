package workload

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// OpenLocal 为签发进程直接申请自身身份，避免网络自调用导致启动环。
func OpenLocal(ctx context.Context, conf Config, issuer *Issuer) (*Identity, error) {
	if issuer == nil {
		return nil, errors.New("workload local issuer required")
	}
	return openIdentity(ctx, conf, func(service string, csr []byte, key string) ([]byte, []byte, error) {
		cert, err := issuer.IssueLocal(service, csr, key)
		return cert, issuer.RootPEM(), err
	})
}

var errExpiredIssuance = errors.New("workload pending issuance expired")

func (i *Identity) refresh(ctx context.Context) (result error) {
	i.attempts.Add(1)
	defer func() {
		if result == nil {
			i.successes.Add(1)
		} else {
			i.failures.Add(1)
		}
	}()
	err := i.refreshOnce(ctx)
	if errors.Is(err, errExpiredIssuance) {
		// 响应曾丢失且返回的幂等结果已过期时，开始新的申请，避免永久重放过期证书。
		i.state.CSRPEM = nil
		i.state.RequestID = ""
		if saveErr := i.saveState(); saveErr != nil {
			return saveErr
		}
		return i.refreshOnce(ctx)
	}
	return err
}

func (i *Identity) refreshOnce(ctx context.Context) error {
	if len(i.state.CSRPEM) == 0 {
		block, _ := pem.Decode(i.state.KeyPEM)
		if block == nil {
			return errors.New("invalid workload private key")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return err
		}
		der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
		if err != nil {
			return err
		}
		nonce := make([]byte, 24)
		if _, err = rand.Read(nonce); err != nil {
			return err
		}
		i.state.CSRPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
		i.state.RequestID = hex.EncodeToString(nonce)
	}
	// 每次发送前确认待申请材料已落盘；上次 fsync 失败不得跳过持久化直接签发。
	if err := i.saveState(); err != nil {
		return err
	}
	var certPEM []byte
	var err error
	if i.local != nil {
		certPEM, _, err = i.local(i.conf.ServiceName, i.state.CSRPEM, i.state.RequestID)
	} else {
		certPEM, err = i.requestCertificate(ctx)
	}
	if err != nil {
		return err
	}
	cert, err := i.parseCertificate(certPEM, true)
	if err != nil {
		old, verifyErr := i.parseCertificate(certPEM, false)
		if verifyErr == nil && !time.Now().Before(old.Leaf.NotAfter) {
			return errExpiredIssuance
		}
		return err
	}
	previous := i.state
	i.state.CertificatePEM = certPEM
	i.state.CSRPEM = nil
	i.state.RequestID = ""
	if err = i.saveState(); err != nil {
		i.state = previous
		return err
	}
	i.mu.Lock()
	i.cert = cert
	i.mu.Unlock()
	return nil
}

func (i *Identity) requestCertificate(ctx context.Context) ([]byte, error) {
	endpoint, err := url.Parse(i.conf.IssuerURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, errors.New("workload issuer_url must be trusted HTTPS URL")
	}
	mode := "enroll"
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: i.roots}
	if i.Valid() {
		mode = "renew"
		tlsConf.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return i.currentCertificate() }
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/" + mode
	body, err := json.Marshal(struct {
		Service   string `json:"service"`
		CSRPEM    string `json:"csr_pem"`
		RequestID string `json:"request_id"`
	}{i.conf.ServiceName, string(i.state.CSRPEM), i.state.RequestID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if mode == "enroll" {
		info, err := os.Stat(i.conf.BootstrapTokenFile)
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("workload bootstrap token must have mode 0600")
		}
		token, err := os.ReadFile(i.conf.BootstrapTokenFile)
		if err != nil {
			return nil, err
		}
		value := strings.TrimSpace(string(token))
		if value == "" {
			return nil, errors.New("empty workload bootstrap token")
		}
		req.Header.Set("Authorization", "Bearer "+value)
	}
	transport := &http.Transport{TLSClientConfig: tlsConf}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("workload issuer redirects forbidden") }}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workload issuer rejected %s: HTTP %d", mode, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var result struct {
		CertificatePEM string `json:"certificate_pem"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("invalid workload issuer response")
	}
	if result.CertificatePEM == "" {
		return nil, errors.New("empty workload issuer certificate")
	}
	return []byte(result.CertificatePEM), nil
}
