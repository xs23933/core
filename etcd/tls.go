package etcd

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"os"
)

// TLSOptions 显式配置 etcd 连接的信任和身份，与业务工作负载证书独立。
type TLSOptions struct {
	Enabled    bool
	CAFile     string
	CertFile   string
	KeyFile    string
	ServerName string
}

// LoadTLS 加载 etcd 凭据并返回内容指纹，用于识别已有 Discovery 的安全配置变化。
func LoadTLS(o TLSOptions) (*tls.Config, string, error) {
	if !o.Enabled {
		if o.CAFile != "" || o.CertFile != "" || o.KeyFile != "" || o.ServerName != "" {
			return nil, "", errors.New("etcd: tls settings require enabled")
		}
		return nil, "", nil
	}
	if o.CAFile == "" {
		return nil, "", errors.New("etcd: tls ca_file required")
	}
	ca, err := os.ReadFile(o.CAFile)
	if err != nil {
		return nil, "", errors.New("etcd: cannot read tls trust bundle")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, "", errors.New("etcd: invalid tls trust bundle")
	}
	c := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: o.ServerName}
	hash := sha256.New()
	_, _ = hash.Write(ca)
	_, _ = hash.Write([]byte("\x00" + o.ServerName))
	if o.CertFile != "" || o.KeyFile != "" {
		if o.CertFile == "" || o.KeyFile == "" {
			return nil, "", errors.New("etcd: tls cert_file and key_file must be paired")
		}
		cert, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
		if err != nil {
			return nil, "", errors.New("etcd: invalid tls client identity")
		}
		c.Certificates = []tls.Certificate{cert}
		for _, der := range cert.Certificate {
			_, _ = hash.Write(der)
		}
	}
	return c, hex.EncodeToString(hash.Sum(nil)), nil
}
