package core

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"github.com/xs23933/core/v3/certificate"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestUseCertificateManagerTLSLifecycle(t *testing.T) {
	app := New(Options{"listen": "127.0.0.1:0"})
	defer app.ShutdownWithTimeout(5 * time.Second)
	manager, err := certificate.New(certificate.Config{StorageDir: t.TempDir(), Authorize: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.UseCertificateManager(manager, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if app.TLSConfig == nil || app.TLSConfig.GetCertificate == nil || !app.TLSConfig.SessionTicketsDisabled {
		t.Fatal("missing authorized TLS callback")
	}
	if app.dynamicCertificateHTTP == nil {
		t.Fatal("challenge listener not attached")
	}
	if err = app.UseCertificateManager(manager, ""); err == nil {
		t.Fatal("duplicate attachment accepted")
	}
	if err = app.prepareTLSForServe(); err != nil {
		t.Fatal(err)
	}
}
func TestUseCertificateManagerRejectsLegacyConflict(t *testing.T) {
	app := New(Options{"acme": Options{"domains": []string{"example.com"}}})
	defer app.ShutdownWithTimeout(5 * time.Second)
	manager, err := certificate.New(certificate.Config{StorageDir: t.TempDir(), Authorize: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err = app.UseCertificateManager(manager, ""); err == nil {
		t.Fatal("legacy ACME conflict accepted")
	}
}

// integrationCertificateEngine 使用本地叶证书，禁止测试访问外部 CA。
type integrationCertificateEngine struct {
	cert   *tls.Certificate
	closed atomic.Bool
}

func (e *integrationCertificateEngine) Load(context.Context, string) (*tls.Certificate, error) {
	return e.cert, nil
}
func (e *integrationCertificateEngine) Obtain(context.Context, string, bool) (*tls.Certificate, error) {
	return nil, errors.New("offline test must not issue certificates")
}
func (e *integrationCertificateEngine) HTTPHandler(next http.Handler) http.Handler { return next }
func (e *integrationCertificateEngine) Close() error                               { e.closed.Store(true); return nil }

type integrationSessionCache struct {
	cache tls.ClientSessionCache
	puts  atomic.Int32
}

func (c *integrationSessionCache) Get(key string) (*tls.ClientSessionState, bool) {
	return c.cache.Get(key)
}
func (c *integrationSessionCache) Put(key string, state *tls.ClientSessionState) {
	c.puts.Add(1)
	c.cache.Put(key, state)
}

func TestUseCertificateManagerServesHTTPSAndRejectsRevokedConnections(t *testing.T) {
	const host = "tls-integration.example.com"
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(100), DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &integrationCertificateEngine{cert: &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey, Leaf: leaf}}
	var revoked atomic.Bool
	var authorizations atomic.Int32
	manager, err := certificate.New(certificate.Config{Engine: fixture, Authorize: func(ctx context.Context, hostname string) error {
		authorizations.Add(1)
		if err := ctx.Err(); err != nil {
			return err
		}
		if hostname != host || revoked.Load() {
			return certificate.ErrUnauthorized
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{"listen": "127.0.0.1:0", "debug": false})
	t.Cleanup(func() {
		if err := app.ShutdownWithTimeout(5 * time.Second); err != nil {
			t.Error(err)
		}
		if err := app.eg.Wait(); err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	// Serve 接收现有 listener；与 Listen 相同，显式设置 Core HTTP handler。
	app.Handler = app
	app.GET("/certificate-probe", func(c Ctx) error { return c.SendString("secured by Core") })
	if err = app.UseCertificateManager(manager, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	httpClosed := make(chan struct{}, 1)
	app.dynamicCertificateHTTP.RegisterOnShutdown(func() { httpClosed <- struct{}{} })
	if err = manager.Ensure(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		status, statusErr := manager.Status(context.Background(), host)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status.State == certificate.StateReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("certificate state %s", status.State)
		}
		time.Sleep(time.Millisecond)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	serveDone := make(chan error, 1)
	go func() { serveDone <- app.Serve(listener) }()
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		cache := &integrationSessionCache{cache: tls.NewLRUClientSessionCache(2)}
		config := &tls.Config{ServerName: host, RootCAs: roots, MinVersion: version, MaxVersion: version, NextProtos: []string{"http/1.1"}, ClientSessionCache: cache}
		request := func() error {
			connection, dialErr := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, config)
			if dialErr != nil {
				return dialErr
			}
			defer connection.Close()
			if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
				return err
			}
			if connection.ConnectionState().DidResume {
				return errors.New("unexpected TLS session resumption")
			}
			req, requestErr := http.NewRequest(http.MethodGet, "https://"+host+"/certificate-probe", nil)
			if requestErr != nil {
				return requestErr
			}
			req.Close = true
			if requestErr = req.Write(connection); requestErr != nil {
				return requestErr
			}
			response, responseErr := http.ReadResponse(bufio.NewReader(connection), req)
			if responseErr != nil {
				return responseErr
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				return readErr
			}
			if response.StatusCode != http.StatusOK || string(body) != "secured by Core" {
				return fmt.Errorf("HTTPS response %d %q", response.StatusCode, body)
			}
			return nil
		}
		revoked.Store(false)
		if err = request(); err != nil {
			t.Fatalf("TLS %x first request: %v", version, err)
		}
		if err = request(); err != nil {
			t.Fatalf("TLS %x second request: %v", version, err)
		}
		if cache.puts.Load() != 0 {
			t.Fatalf("TLS %x server issued session tickets", version)
		}
		before := authorizations.Load()
		revoked.Store(true)
		if err = request(); err == nil {
			t.Fatalf("TLS %x revoked connection succeeded", version)
		}
		if authorizations.Load() <= before {
			t.Fatalf("TLS %x revoked connection skipped current authorization", version)
		}
	}
	if err = app.ShutdownWithTimeout(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Core TLS listener did not stop")
	}
	select {
	case <-httpClosed:
	case <-time.After(time.Second):
		t.Fatal("Core HTTP challenge server did not shut down")
	}
	if !fixture.closed.Load() {
		t.Fatal("Core did not close certificate engine")
	}
	if _, err = manager.Status(context.Background(), host); !errors.Is(err, certificate.ErrClosed) {
		t.Fatalf("manager still open: %v", err)
	}
	connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("HTTPS listener remained open after shutdown")
	}
}
