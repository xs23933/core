package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

func TestStartGRPCServerRejectsCredentialsOnSharedAddress(t *testing.T) {
	serverTLS, _, _ := mutualTLSConfigsForTest(t)
	app := New(Options{"listen": "127.0.0.1:0"})
	app.EnableGRPC("127.0.0.1:0")
	if err := app.ConfigureGRPCServer(GRPCServerConfig{
		TransportCredentials: credentials.NewTLS(serverTLS),
	}); err != nil {
		t.Fatalf("configure gRPC server: %v", err)
	}
	_ = app.GetGRPCServer()
	if err := app.startGRPCServer(); err != ErrGRPCTLSSharedAddress {
		t.Fatalf("start error = %v, want %v", err, ErrGRPCTLSSharedAddress)
	}
}

func TestCoreGRPCServerMutualTLS(t *testing.T) {
	serverTLS, trustedClientTLS, untrustedClientTLS := mutualTLSConfigsForTest(t)
	app := New()
	if err := app.ConfigureGRPCServer(GRPCServerConfig{
		TransportCredentials: credentials.NewTLS(serverTLS),
	}); err != nil {
		t.Fatalf("configure gRPC server: %v", err)
	}

	var handlerCalls atomic.Int64
	server := app.GetGRPCServer()
	healthpb.RegisterHealthServer(server, &countingHealthServer{calls: &handlerCalls})
	listener := bufconn.Listen(1024 * 1024)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	t.Run("trusted client", func(t *testing.T) {
		client := newTLSHealthClient(t, listener, trustedClientTLS)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := client.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
			t.Fatalf("trusted health check: %v", err)
		}
	})
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("handler calls after trusted client = %d, want 1", got)
	}

	t.Run("missing client certificate", func(t *testing.T) {
		withoutCertificate := trustedClientTLS.Clone()
		withoutCertificate.Certificates = nil
		client := newTLSHealthClient(t, listener, withoutCertificate)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := client.Check(ctx, &healthpb.HealthCheckRequest{}); err == nil {
			t.Fatal("health check without client certificate succeeded")
		}
	})

	t.Run("untrusted client certificate", func(t *testing.T) {
		client := newTLSHealthClient(t, listener, untrustedClientTLS)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := client.Check(ctx, &healthpb.HealthCheckRequest{}); err == nil {
			t.Fatal("health check with untrusted client certificate succeeded")
		}
	})
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("untrusted clients reached handler: calls = %d, want 1", got)
	}
}

type countingHealthServer struct {
	healthpb.UnimplementedHealthServer
	calls *atomic.Int64
}

func (server *countingHealthServer) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	server.calls.Add(1)
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func newTLSHealthClient(t *testing.T, listener *bufconn.Listener, config *tls.Config) healthpb.HealthClient {
	t.Helper()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(credentials.NewTLS(config)),
	)
	if err != nil {
		t.Fatalf("create TLS gRPC client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return healthpb.NewHealthClient(conn)
}

func mutualTLSConfigsForTest(t *testing.T) (*tls.Config, *tls.Config, *tls.Config) {
	t.Helper()
	trustedCA, trustedCAKey := issueCertificateAuthority(t, "trusted-root")
	untrustedCA, untrustedCAKey := issueCertificateAuthority(t, "untrusted-root")
	serverCertificate := issueCertificate(t, trustedCA, trustedCAKey, "wallet.test", []string{"wallet.test"}, x509.ExtKeyUsageServerAuth)
	trustedClientCertificate := issueCertificate(t, trustedCA, trustedCAKey, "trusted-client", nil, x509.ExtKeyUsageClientAuth)
	untrustedClientCertificate := issueCertificate(t, untrustedCA, untrustedCAKey, "untrusted-client", nil, x509.ExtKeyUsageClientAuth)

	trustedPool := x509.NewCertPool()
	trustedPool.AddCert(trustedCA)
	serverTLS := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    trustedPool,
	}
	trustedClientTLS := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   "wallet.test",
		RootCAs:      trustedPool,
		Certificates: []tls.Certificate{trustedClientCertificate},
	}
	untrustedClientTLS := trustedClientTLS.Clone()
	untrustedClientTLS.Certificates = []tls.Certificate{untrustedClientCertificate}
	return serverTLS, trustedClientTLS, untrustedClientTLS
}

func issueCertificateAuthority(t *testing.T, commonName string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          randomSerialNumber(t),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	return certificate, key
}

func issueCertificate(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, commonName string, dnsNames []string, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate certificate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: randomSerialNumber(t),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: certificate}
}

func randomSerialNumber(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate certificate serial: %v", err)
	}
	return serial
}
