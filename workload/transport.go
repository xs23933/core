package workload

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"regexp"
	"time"

	"google.golang.org/grpc/credentials"
)

var identityPart = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

func workloadURI(domain, service string) (string, error) {
	if !identityPart.MatchString(domain) || !identityPart.MatchString(service) {
		return "", errors.New("invalid workload trust domain or service name")
	}
	return "spiffe://" + domain + "/" + service, nil
}

func verifyWorkload(raw [][]byte, roots *x509.CertPool, domain, target string, usage x509.ExtKeyUsage, now time.Time) error {
	if len(raw) == 0 {
		return errors.New("workload peer certificate required")
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	intermediates := x509.NewCertPool()
	for _, der := range raw[1:] {
		cert, e := x509.ParseCertificate(der)
		if e != nil {
			return e
		}
		intermediates.AddCert(cert)
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
		return fmt.Errorf("workload certificate verification: %w", err)
	}
	if leaf.IsCA || len(leaf.URIs) != 1 {
		return errors.New("workload requires one non-CA URI identity")
	}
	uri := leaf.URIs[0]
	if uri.Scheme != "spiffe" || uri.Host != domain || uri.User != nil || uri.RawQuery != "" || uri.Fragment != "" || uri.RawPath != "" {
		return errors.New("invalid workload peer URI")
	}
	service := uri.Path
	if len(service) < 2 {
		return errors.New("missing workload peer service")
	}
	service = service[1:]
	expected, err := workloadURI(domain, service)
	if err != nil {
		return err
	}
	if uri.String() != expected {
		return errors.New("noncanonical workload peer URI")
	}
	if target != "" {
		wanted, err := workloadURI(domain, target)
		if err != nil {
			return err
		}
		if expected != wanted {
			return errors.New("unexpected workload peer identity")
		}
	}
	// 必须显式声明用途，拒绝省略 EKU 或仅使用 Any 的外部证书。
	found := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == usage {
			found = true
		}
	}
	if !found {
		return errors.New("workload certificate missing required EKU")
	}
	return nil
}

func (i *Identity) currentCertificate() (*tls.Certificate, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.closed || i.cert == nil || time.Now().Before(i.cert.Leaf.NotBefore) || !time.Now().Before(i.cert.Leaf.NotAfter) {
		return nil, errors.New("workload identity is unavailable or expired")
	}
	return i.cert, nil
}

// ClientTLSConfig 根据逻辑服务身份验证对端，地址变化不要求重发证书。
func (i *Identity) ClientTLSConfig(targetService string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: i.roots,
		// URI 身份不能使用 DNS 校验，下面完整替代证书链、用途、期限和身份校验。
		InsecureSkipVerify:   true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return i.currentCertificate() },
		VerifyConnection: func(state tls.ConnectionState) error {
			if _, err := i.currentCertificate(); err != nil {
				return err
			}
			if _, err := workloadURI(i.conf.TrustDomain, targetService); err != nil {
				return err
			}
			raw := make([][]byte, len(state.PeerCertificates))
			for n, cert := range state.PeerCertificates {
				raw[n] = cert.Raw
			}
			return verifyWorkload(raw, i.roots, i.conf.TrustDomain, targetService, x509.ExtKeyUsageServerAuth, time.Now())
		},
	}
}

// ServerTLSConfig 为每次握手读取当前证书并严格验证调用方工作负载身份。
func (i *Identity) ServerTLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: i.roots,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return i.currentCertificate() },
		VerifyConnection: func(state tls.ConnectionState) error {
			raw := make([][]byte, len(state.PeerCertificates))
			for n, cert := range state.PeerCertificates {
				raw[n] = cert.Raw
			}
			return verifyWorkload(raw, i.roots, i.conf.TrustDomain, "", x509.ExtKeyUsageClientAuth, time.Now())
		},
	}
}

// ClientCredentials 的克隆仍引用动态身份，避免连接重建时固化旧证书。
func (i *Identity) ClientCredentials(targetService string) credentials.TransportCredentials {
	return credentials.NewTLS(i.ClientTLSConfig(targetService))
}

// ServerCredentials 返回托管服务端凭据。
func (i *Identity) ServerCredentials() credentials.TransportCredentials {
	return credentials.NewTLS(i.ServerTLSConfig())
}
