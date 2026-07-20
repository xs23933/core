package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	core "github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

func TestServicePoolMixedTransport(t *testing.T) {
	walletAddress, walletRoots := startGatewayReflectionServer(t, true)
	merchantAddress, _ := startGatewayReflectionServer(t, false)
	app := core.New()
	if err := app.ConfigureGRPCClient("xpay-wallet", core.GRPCClientConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    walletRoots,
			ServerName: "wallet.test",
		}),
	}); err != nil {
		t.Fatalf("configure wallet credentials: %v", err)
	}

	merchantProxy, _, err := NewServicePool(app, "xpay-merchant").AddOrUpdateInstance("merchant-1", merchantAddress)
	if err != nil {
		t.Fatalf("connect plaintext merchant: %v", err)
	}
	assertGatewayHealthMethod(t, merchantProxy)

	walletProxy, _, err := NewServicePool(app, "xpay-wallet").AddOrUpdateInstance("wallet-1", walletAddress)
	if err != nil {
		t.Fatalf("connect TLS wallet: %v", err)
	}
	assertGatewayHealthMethod(t, walletProxy)
}

func TestReflectionProxyServiceCredentialsStayIsolated(t *testing.T) {
	walletAddress, walletRoots := startGatewayReflectionServer(t, true)
	app := core.New()
	if err := app.ConfigureGRPCClient("xpay-wallet", core.GRPCClientConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    walletRoots,
			ServerName: "wallet.test",
		}),
	}); err != nil {
		t.Fatalf("configure wallet credentials: %v", err)
	}

	walletProxy, _, err := NewServicePool(app, "xpay-wallet").AddOrUpdateInstance("wallet-1", walletAddress)
	if err != nil {
		t.Fatalf("connect TLS wallet: %v", err)
	}
	assertGatewayHealthMethod(t, walletProxy)

	if _, _, err := NewServicePool(app, "xpay-merchant").AddOrUpdateInstance("merchant-1", walletAddress); err == nil {
		t.Fatal("unconfigured plaintext service connected to TLS wallet address")
	}
}

func TestServicePoolWalletTLSFailureDoesNotBreakPlaintextService(t *testing.T) {
	walletAddress, _ := startGatewayReflectionServer(t, true)
	merchantAddress, _ := startGatewayReflectionServer(t, false)
	_, wrongRoots := startGatewayReflectionServer(t, true)
	app := core.New()
	if err := app.ConfigureGRPCClient("xpay-wallet", core.GRPCClientConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    wrongRoots,
			ServerName: "wallet.test",
		}),
	}); err != nil {
		t.Fatalf("configure wallet credentials: %v", err)
	}

	if _, _, err := NewServicePool(app, "xpay-wallet").AddOrUpdateInstance("wallet-1", walletAddress); err == nil {
		t.Fatal("wallet connection with wrong CA succeeded")
	}
	merchantProxy, _, err := NewServicePool(app, "xpay-merchant").AddOrUpdateInstance("merchant-1", merchantAddress)
	if err != nil {
		t.Fatalf("plaintext merchant failed after wallet TLS error: %v", err)
	}
	assertGatewayHealthMethod(t, merchantProxy)
}

func assertGatewayHealthMethod(t *testing.T, proxy *ReflectionProxy) {
	t.Helper()
	if _, ok := proxy.Methods()["/grpc.health.v1.Health/Check"]; !ok {
		t.Fatalf("methods = %#v, want health Check", proxy.Methods())
	}
}

func startGatewayReflectionServer(t *testing.T, withTLS bool) (string, *x509.CertPool) {
	t.Helper()
	var options []grpc.ServerOption
	var roots *x509.CertPool
	if withTLS {
		root, rootKey := issueGatewayTestCA(t)
		certificate := issueGatewayTestServerCertificate(t, root, rootKey)
		roots = x509.NewCertPool()
		roots.AddCert(root)
		options = append(options, grpc.Creds(credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{certificate},
		})))
	}
	server := grpc.NewServer(options...)
	healthpb.RegisterHealthServer(server, &gatewayTestHealthServer{})
	reflection.Register(server)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String(), roots
}

type gatewayTestHealthServer struct {
	healthpb.UnimplementedHealthServer
}

func issueGatewayTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          gatewayTestSerial(t),
		Subject:               pkix.Name{CommonName: "gateway-test-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	return certificate, key
}

func issueGatewayTestServerCertificate(t *testing.T, root *x509.Certificate, rootKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: gatewayTestSerial(t),
		Subject:      pkix.Name{CommonName: "wallet.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"wallet.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func gatewayTestSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	return serial
}
