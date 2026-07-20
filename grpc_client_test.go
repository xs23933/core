package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestConfigureGRPCClientLifecycle(t *testing.T) {
	_, rootPool := startClientTLSTestServer(t)
	newCredentials := func(serverName string) credentials.TransportCredentials {
		return credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    rootPool,
			ServerName: serverName,
		})
	}

	t.Run("requires service name", func(t *testing.T) {
		err := New().ConfigureGRPCClient(" \t", GRPCClientConfig{TransportCredentials: newCredentials("wallet.test")})
		if !errors.Is(err, ErrGRPCClientServiceRequired) {
			t.Fatalf("error = %v, want %v", err, ErrGRPCClientServiceRequired)
		}
	})

	t.Run("requires credentials", func(t *testing.T) {
		err := New().ConfigureGRPCClient("xpay-wallet", GRPCClientConfig{})
		if !errors.Is(err, ErrGRPCClientCredentialsRequired) {
			t.Fatalf("error = %v, want %v", err, ErrGRPCClientCredentialsRequired)
		}
	})

	t.Run("rejects second configuration", func(t *testing.T) {
		app := New()
		config := GRPCClientConfig{TransportCredentials: newCredentials("wallet.test")}
		if err := app.ConfigureGRPCClient(" xpay-wallet ", config); err != nil {
			t.Fatalf("configure: %v", err)
		}
		if err := app.ConfigureGRPCClient("xpay-wallet", config); !errors.Is(err, ErrGRPCClientAlreadyConfigured) {
			t.Fatalf("second configure error = %v, want %v", err, ErrGRPCClientAlreadyConfigured)
		}
	})

	t.Run("rejects configuration after first dial", func(t *testing.T) {
		address := startClientPlaintextTestServer(t)
		app := New()
		conn, err := app.GrpcClientAt("xpay-wallet", address)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := app.ConfigureGRPCClient("xpay-wallet", GRPCClientConfig{TransportCredentials: newCredentials("wallet.test")}); !errors.Is(err, ErrGRPCClientAlreadyInitialized) {
			t.Fatalf("late configure error = %v, want %v", err, ErrGRPCClientAlreadyInitialized)
		}
	})

	t.Run("rejects target and opaque configured options", func(t *testing.T) {
		app := New()
		if _, err := app.GrpcClientAt("xpay-wallet", " \t"); !errors.Is(err, ErrGRPCClientTargetRequired) {
			t.Fatalf("empty target error = %v, want %v", err, ErrGRPCClientTargetRequired)
		}
		if err := app.ConfigureGRPCClient("xpay-wallet", GRPCClientConfig{TransportCredentials: newCredentials("wallet.test")}); err != nil {
			t.Fatalf("configure: %v", err)
		}
		if _, err := app.GrpcClient("xpay-wallet", grpc.WithBlock()); !errors.Is(err, ErrGRPCClientDialOptionsConflict) {
			t.Fatalf("configured options error = %v, want %v", err, ErrGRPCClientDialOptionsConflict)
		}
	})
}

func TestGrpcClientAtUsesConfiguredTLSWithoutFallback(t *testing.T) {
	address, rootPool := startClientTLSTestServer(t)
	app := New()
	if err := app.ConfigureGRPCClient("xpay-wallet", GRPCClientConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    rootPool,
			ServerName: "wallet.test",
		}),
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	conn, err := app.GrpcClientAt("xpay-wallet", address)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	assertClientHealthRPC(t, conn, true)
}

func TestGrpcClientAtRejectsWrongTLSServerIdentity(t *testing.T) {
	address, rootPool := startClientTLSTestServer(t)
	app := New()
	if err := app.ConfigureGRPCClient("xpay-wallet", GRPCClientConfig{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    rootPool,
			ServerName: "wrong.test",
		}),
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	conn, err := app.GrpcClientAt("xpay-wallet", address)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	assertClientHealthRPC(t, conn, false)
}

func TestGrpcClientAtDefaultsUnconfiguredServiceToPlaintext(t *testing.T) {
	address := startClientPlaintextTestServer(t)
	conn, err := New().GrpcClientAt("xpay-merchant", address)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	assertClientHealthRPC(t, conn, true)
}

func TestGrpcClientAtClonesConfiguredCredentialsForEveryConnection(t *testing.T) {
	address, rootPool := startClientTLSTestServer(t)
	var clones atomic.Int64
	configured := &recordingTransportCredentials{
		delegate: credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    rootPool,
			ServerName: "wallet.test",
		}),
		clones: &clones,
	}
	app := New()
	if err := app.ConfigureGRPCClient("xpay-wallet", GRPCClientConfig{TransportCredentials: configured}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	clones.Store(0)
	for range 2 {
		conn, err := app.GrpcClientAt("xpay-wallet", address)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		assertClientHealthRPC(t, conn, true)
		_ = conn.Close()
	}
	if got := clones.Load(); got != 2 {
		t.Fatalf("Clone calls = %d, want 2", got)
	}
}

func TestConfigureGRPCClientSnapshotsTransportCredentials(t *testing.T) {
	address, rootPool := startClientTLSTestServer(t)
	configured := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    rootPool,
		ServerName: "wallet.test",
	})
	app := New()
	if err := app.ConfigureGRPCClient("xpay-wallet", GRPCClientConfig{TransportCredentials: configured}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if err := configured.OverrideServerName("wrong.test"); err != nil {
		t.Fatalf("mutate caller credential: %v", err)
	}
	conn, err := app.GrpcClientAt("xpay-wallet", address)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	assertClientHealthRPC(t, conn, true)
}

type recordingTransportCredentials struct {
	delegate credentials.TransportCredentials
	clones   *atomic.Int64
}

func (credential *recordingTransportCredentials) ClientHandshake(ctx context.Context, authority string, rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return credential.delegate.ClientHandshake(ctx, authority, rawConn)
}

func (credential *recordingTransportCredentials) ServerHandshake(rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return credential.delegate.ServerHandshake(rawConn)
}

func (credential *recordingTransportCredentials) Info() credentials.ProtocolInfo {
	return credential.delegate.Info()
}

func (credential *recordingTransportCredentials) Clone() credentials.TransportCredentials {
	credential.clones.Add(1)
	return &recordingTransportCredentials{delegate: credential.delegate.Clone(), clones: credential.clones}
}

func (credential *recordingTransportCredentials) OverrideServerName(serverNameOverride string) error {
	return credential.delegate.OverrideServerName(serverNameOverride)
}

func startClientTLSTestServer(t *testing.T) (string, *x509.CertPool) {
	t.Helper()
	root, rootKey := issueCertificateAuthority(t, "client-test-root")
	certificate := issueCertificate(t, root, rootKey, "wallet.test", []string{"wallet.test"}, x509.ExtKeyUsageServerAuth)
	pool := x509.NewCertPool()
	pool.AddCert(root)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
	})))
	return startClientHealthServer(t, server), pool
}

func startClientPlaintextTestServer(t *testing.T) string {
	t.Helper()
	return startClientHealthServer(t, grpc.NewServer())
}

func startClientHealthServer(t *testing.T, server *grpc.Server) string {
	t.Helper()
	healthpb.RegisterHealthServer(server, &countingHealthServer{calls: &atomic.Int64{}})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String()
}

func assertClientHealthRPC(t *testing.T, conn *grpc.ClientConn, wantSuccess bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if wantSuccess && err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if !wantSuccess && err == nil {
		t.Fatal("health check unexpectedly succeeded")
	}
}

var _ credentials.TransportCredentials = (*recordingTransportCredentials)(nil)
