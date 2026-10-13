package gateway

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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

func etcdTLSFixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated etcd root"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	ca, e = x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	rootPath := filepath.Join(dir, "root.pem")
	if e = os.WriteFile(rootPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	leafKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "isolated-etcd"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, e = x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	private, e := x509.MarshalPKCS8PrivateKey(leafKey)
	if e != nil {
		t.Fatal(e)
	}
	certPath, keyPath := filepath.Join(dir, "leaf.pem"), filepath.Join(dir, "key.pem")
	if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); e != nil {
		t.Fatal(e)
	}
	return rootPath, certPath, keyPath
}

// TestEtcdMutualTLSThreeEntrypoints 验证注册、发现和 Gateway 自建连接全部使用显式 etcd mTLS。
func TestEtcdMutualTLSThreeEntrypoints(t *testing.T) {
	root, cert, key := etcdTLSFixture(t)
	config := embed.NewConfig()
	config.Dir = t.TempDir()
	config.LogLevel = "error"
	clientURL := url.URL{Scheme: "https", Host: workloadTestAddress(t)}
	peerURL := url.URL{Scheme: "http", Host: workloadTestAddress(t)}
	config.ListenClientUrls = []url.URL{clientURL}
	config.AdvertiseClientUrls = []url.URL{clientURL}
	config.ListenPeerUrls = []url.URL{peerURL}
	config.AdvertisePeerUrls = []url.URL{peerURL}
	config.InitialCluster = config.Name + "=" + peerURL.String()
	config.ClientTLSInfo = transport.TLSInfo{CertFile: cert, KeyFile: key, TrustedCAFile: root, ClientCertAuth: true}
	server, e := embed.StartEtcd(config)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(server.Close)
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		t.Fatal("TLS etcd未就绪")
	}
	options := func(name string) core.Options {
		return core.Options{"debug": false, "listen": "127.0.0.1:0", "etcd": core.Options{"endpoints": []string{clientURL.String()}, "namespace": "isolated-etcd-tls", "service_name": name, "service_addr": "127.0.0.1:39001", "service_id": "one", "tls": core.Options{"enabled": true, "ca_file": root, "cert_file": cert, "key_file": key}}}
	}
	registry, discovery, gatewayApp := core.New(options("backend")), core.New(options("client")), core.New(options("gateway"))
	for _, app := range []*core.Core{registry, discovery, gatewayApp} {
		t.Cleanup(func() { _ = app.ShutdownWithTimeout(time.Second) })
	}
	if e = registry.EnableEtcdRegistry(nil); e != nil {
		t.Fatalf("mTLS Registry: %v", e)
	}
	if e = discovery.EnableEtcdDiscovery(nil); e != nil {
		t.Fatalf("mTLS Discovery: %v", e)
	}
	if e = discovery.EtcdDiscovery.Watch("backend"); e != nil {
		t.Fatalf("mTLS Discovery watch: %v", e)
	}
	if len(discovery.EtcdDiscovery.GetServices("backend")) != 1 {
		t.Fatal("mTLS Discovery未读取注册实例")
	}
	gw, e := NewEtcdGateway(gatewayApp)
	if e != nil {
		t.Fatalf("mTLS Gateway: %v", e)
	}
	t.Cleanup(func() { _ = gw.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, e := gw.etcdCli.Get(ctx, "/isolated-etcd-tls/services/backend/", clientv3.WithPrefix())
	if e != nil || len(response.Kvs) != 1 {
		t.Fatalf("Gateway自建TLS client未读取注册: %v", e)
	}
	// 同一信任根但不携带客户端证书，必须被 etcd 拒绝。
	caBytes, e := os.ReadFile(root)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caBytes)
	anonymous, e := clientv3.New(clientv3.Config{Endpoints: []string{clientURL.String()}, TLS: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}, DialTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	defer anonymous.Close()
	deniedCtx, deniedCancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer deniedCancel()
	if _, e = anonymous.Get(deniedCtx, "/isolated-etcd-tls/", clientv3.WithPrefix()); e == nil {
		t.Fatal("etcd接受了无客户端身份的连接")
	}
}
