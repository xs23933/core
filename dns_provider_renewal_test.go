package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
)

// Exercise the maintenance callback, not a direct RenewCertSync call. A newer
// on-disk cert must be reloaded from the instance's storage without any CA call.
func TestCertMagicMaintenanceUsesInstanceStorage(t *testing.T) {
	app := New()
	app.certMagicConfig.CacheDir = t.TempDir()
	magic, cache := app.newCertMagicConfig()
	defer cache.Stop()
	magic.DisableARI = true
	ctx := context.Background()
	host := "renewal.example.com"
	issuer := magic.Issuers[0].IssuerKey()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	store := func(k string, data []byte) {
		t.Helper()
		if err := magic.Storage.Store(ctx, k, data); err != nil {
			t.Fatal(err)
		}
	}
	store(certmagic.StorageKeys.SitePrivateKey(issuer, host), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	store(certmagic.StorageKeys.SiteMeta(issuer, host), []byte(`{"sans":["renewal.example.com"]}`))
	now := time.Now()
	save := func(serial int64, expiry time.Time) {
		t.Helper()
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: now.Add(-60 * 24 * time.Hour), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		store(certmagic.StorageKeys.SiteCert(issuer, host), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	save(1, now.Add(time.Hour))
	cached, err := magic.CacheManagedCertificate(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.NeedsRenewal(magic) {
		t.Fatal("fixture is not due for renewal")
	}
	save(2, now.Add(90*24*time.Hour))
	if err := cache.RenewManagedCertificates(ctx); err != nil {
		t.Fatal(err)
	}
	served, err := magic.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(served.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.SerialNumber.Int64() != 2 {
		t.Fatalf("maintenance served old serial %s; expected renewed cert from instance storage", leaf.SerialNumber)
	}
}
