package workload

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type issuerRecord struct {
	Service        string `json:"service"`
	CSRDigest      string `json:"csr_digest"`
	CertificatePEM string `json:"certificate_pem"`
	IssuedAt       string `json:"issued_at"`
	Serial         string `json:"serial"`
	Kind           string `json:"kind"`
}
type issuerState struct {
	Version            int                     `json:"version"`
	TrustDomain        string                  `json:"trust_domain"`
	AdvertiseURL       string                  `json:"advertise_url"`
	RootPEM            string                  `json:"root_pem"`
	IntermediatePEM    string                  `json:"intermediate_pem"`
	IntermediateKeyPEM string                  `json:"intermediate_key_pem"`
	HTTPSPEM           string                  `json:"https_pem"`
	HTTPSKeyPEM        string                  `json:"https_key_pem"`
	Records            map[string]issuerRecord `json:"records"`
}

func issuerAtomicWrite(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".issuer-pending-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func issuerSecureRead(path string) ([]byte, error) {
	i, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("issuer file requires regular file with private permissions: %s", path)
	}
	return os.ReadFile(path)
}
func issuerSave(path string, s issuerState) error {
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	return issuerAtomicWrite(path, b)
}
func issuerLock(dir string) (*os.File, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	i, e := os.Lstat(dir)
	if e != nil {
		return nil, e
	}
	if !i.IsDir() || i.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("issuer state directory requires 0700 permissions")
	}
	f, e := os.OpenFile(filepath.Join(dir, "issuer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("issuer state already locked: %w", e)
	}
	return f, nil
}
func issuerUnlock(f *os.File) error {
	if f == nil {
		return nil
	}
	e := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}

// issuerCreatePin 使用排他创建，避免不同状态目录的首次初始化覆盖同一个部署指纹。
func issuerCreatePin(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeError := f.Close()
	if e != nil {
		return e
	}
	if closeError != nil {
		return closeError
	}
	directory, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer directory.Close()
	return directory.Sync()
}
