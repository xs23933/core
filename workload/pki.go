package workload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"regexp"
	"time"
)

var issuerServiceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var issuerDomain = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,252}$`)

func issuerSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
func issuerKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }
func issuerKeyPEM(k *ecdsa.PrivateKey) ([]byte, error) {
	b, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		return nil, e
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}), nil
}
func issuerParseKey(b []byte) (*ecdsa.PrivateKey, error) {
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, fmt.Errorf("invalid private key PEM")
	}
	k, e := x509.ParsePKCS8PrivateKey(p.Bytes)
	if e != nil {
		return nil, e
	}
	v, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected ECDSA key")
	}
	return v, nil
}
func issuerParseCert(b []byte) (*x509.Certificate, error) {
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, fmt.Errorf("invalid certificate PEM")
	}
	return x509.ParseCertificate(p.Bytes)
}
func issuerCert(t, p *x509.Certificate, key any, parentKey any) ([]byte, error) {
	der, e := x509.CreateCertificate(rand.Reader, t, p, key, parentKey)
	if e != nil {
		return nil, e
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
func issuerFingerprint(b []byte) (string, error) {
	c, e := issuerParseCert(b)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(c.Raw)
	return hex.EncodeToString(h[:]), nil
}

// issuerCreatePKI 只在首次安装时创建私有信任根，后续启动必须加载已提交状态。
func issuerCreatePKI(c IssuerConfig) (issuerState, []byte, error) {
	var s issuerState
	now := time.Now()
	rootKey, e := issuerKey()
	if e != nil {
		return s, nil, e
	}
	serial, e := issuerSerial()
	if e != nil {
		return s, nil, e
	}
	root := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: c.TrustDomain + " workload root"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLen: 1, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	rootPEM, e := issuerCert(root, root, &rootKey.PublicKey, rootKey)
	if e != nil {
		return s, nil, e
	}
	root, e = issuerParseCert(rootPEM)
	if e != nil {
		return s, nil, e
	}
	intermediateKey, e := issuerKey()
	if e != nil {
		return s, nil, e
	}
	serial, e = issuerSerial()
	if e != nil {
		return s, nil, e
	}
	intermediate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: c.TrustDomain + " workload issuer"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(2, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	interPEM, e := issuerCert(intermediate, root, &intermediateKey.PublicKey, rootKey)
	if e != nil {
		return s, nil, e
	}
	intermediate, e = issuerParseCert(interPEM)
	if e != nil {
		return s, nil, e
	}
	httpsKey, e := issuerKey()
	if e != nil {
		return s, nil, e
	}
	serial, e = issuerSerial()
	if e != nil {
		return s, nil, e
	}
	u, e := url.Parse(c.AdvertiseURL)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" {
		return s, nil, fmt.Errorf("issuer advertise URL must be HTTPS")
	}
	https := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "workload enrollment"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		https.IPAddresses = []net.IP{ip}
	} else {
		https.DNSNames = []string{u.Hostname()}
	}
	httpsPEM, e := issuerCert(https, intermediate, &httpsKey.PublicKey, intermediateKey)
	if e != nil {
		return s, nil, e
	}
	interKeyPEM, e := issuerKeyPEM(intermediateKey)
	if e != nil {
		return s, nil, e
	}
	httpsKeyPEM, e := issuerKeyPEM(httpsKey)
	if e != nil {
		return s, nil, e
	}
	rootKeyPEM, e := issuerKeyPEM(rootKey)
	if e != nil {
		return s, nil, e
	}
	s = issuerState{Version: 1, TrustDomain: c.TrustDomain, AdvertiseURL: c.AdvertiseURL, RootPEM: string(rootPEM), IntermediatePEM: string(interPEM), IntermediateKeyPEM: string(interKeyPEM), HTTPSPEM: string(append(httpsPEM, interPEM...)), HTTPSKeyPEM: string(httpsKeyPEM), Records: map[string]issuerRecord{}}
	return s, rootKeyPEM, nil
}
