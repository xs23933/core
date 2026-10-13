package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/workload"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func workloadTestAddress(t *testing.T) string {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := ln.Addr().String()
	if e = ln.Close(); e != nil {
		t.Fatal(e)
	}
	return address
}
func workloadTestEtcdProcess(t *testing.T) string {
	t.Helper()
	binary, e := exec.LookPath("etcd")
	if e != nil {
		if _, e = os.Stat("/opt/homebrew/bin/etcd"); e != nil {
			t.Skip("真实 etcd 可执行文件未安装")
		}
		binary = "/opt/homebrew/bin/etcd"
	}
	clientAddress, peerAddress := workloadTestAddress(t), workloadTestAddress(t)
	endpoint := "http://" + clientAddress
	peer := "http://" + peerAddress
	logFile, e := os.Create(filepath.Join(t.TempDir(), "etcd.log"))
	if e != nil {
		t.Fatal(e)
	}
	command := exec.Command(binary, "--name=workload-test", "--data-dir="+t.TempDir(), "--listen-client-urls="+endpoint, "--advertise-client-urls="+endpoint, "--listen-peer-urls="+peer, "--initial-advertise-peer-urls="+peer, "--initial-cluster=workload-test="+peer, "--log-level=error")
	command.Stdout = logFile
	command.Stderr = logFile
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); _ = logFile.Close() })
	client, e := clientv3.New(clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, e = client.Status(ctx, endpoint)
		cancel()
		if e == nil {
			return endpoint
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("真实 etcd 进程未就绪: %v", e)
	return ""
}
func workloadTestServe(t *testing.T, app *core.Core) {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- app.Serve(ln) }()
	t.Cleanup(func() {
		if e := app.ShutdownWithTimeout(3 * time.Second); e != nil {
			t.Error(e)
		}
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(4 * time.Second):
			t.Error("应用退出超时")
		}
	})
}

// TestWorkloadGatewayRealEtcd 验证真实发现、HTTPS 申请、reflection 和后端直连共用身份。
func TestWorkloadGatewayRealEtcd(t *testing.T) {
	endpoint := workloadTestEtcdProcess(t)
	base := t.TempDir()
	issuerAddress := workloadTestAddress(t)
	issuerURL := "https://" + issuerAddress
	namespace := fmt.Sprintf("workload-test-%d", time.Now().UnixNano())
	policy := workload.EnrollmentPolicy{Services: map[string]workload.EnrollmentBinding{}}
	tokens := map[string]string{}
	for _, service := range []string{"echo", "caller"} {
		token := "isolated-test-bootstrap-" + service + "-0123456789abcdef"
		digest := sha256.Sum256([]byte(token))
		tokens[service] = filepath.Join(base, service+".token")
		if e := os.WriteFile(tokens[service], []byte(token), 0600); e != nil {
			t.Fatal(e)
		}
		policy.Services[service] = workload.EnrollmentBinding{TokenSHA256: hex.EncodeToString(digest[:])}
	}
	policyFile := filepath.Join(base, "policy.json")
	b, e := json.Marshal(policy)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(policyFile, b, 0600); e != nil {
		t.Fatal(e)
	}
	issuerState := filepath.Join(base, "issuer")
	roots := filepath.Join(issuerState, "root.pem")
	rootPin := ""

	common := func(service, state string) core.Options {
		identityConfig := core.Options{"enabled": true, "service_name": service, "trust_domain": "integration", "issuer_url": issuerURL, "root_sha256": rootPin, "bootstrap_token_file": tokens[service], "state_dir": state}
		if service == "gateway" {
			identityConfig["trust_bundle_file"] = roots
		}
		return core.Options{"debug": false, "listen": "127.0.0.1:0", "workload": identityConfig, "etcd": core.Options{"endpoints": []string{endpoint}, "namespace": namespace, "service_name": service}}
	}
	gatewayOptions := common("gateway", filepath.Join(base, "gateway-identity"))
	gatewayOptions["workload_issuer"] = core.Options{"enabled": true, "state_dir": issuerState, "pin_file": filepath.Join(base, "root.pin"), "policy_file": policyFile, "listen": issuerAddress, "advertise_url": issuerURL}
	gatewayApp := core.New(gatewayOptions)
	type preparedBackend struct {
		app     *core.Core
		address string
	}
	var prepared []preparedBackend
	echoState := filepath.Join(base, "echo-identity")
	for _, spec := range []struct{ service, id, state string }{{"echo", "one", echoState}, {"caller", "one", filepath.Join(base, "caller-identity")}, {"echo", "two", filepath.Join(base, "echo-replica")}, {"echo", "one", echoState}, {"echo", "one", echoState}} {
		address := workloadTestAddress(t)
		options := common(spec.service, spec.state)
		options["etcd"].(core.Options)["service_id"] = spec.id
		options["etcd"].(core.Options)["service_addr"] = address
		prepared = append(prepared, preparedBackend{core.New(options), address})
	}
	nextBackend := 0
	if e = gatewayApp.EnableWorkloadIdentity(); e != nil {
		t.Fatal(e)
	}
	// Gateway 首次启动自动创建 CA；测试部署随后仅读可信本地指纹，后端没有共享根文件。
	pinBytes, pinError := os.ReadFile(filepath.Join(base, "root.pin"))
	if pinError != nil {
		t.Fatal(pinError)
	}
	rootPin = strings.TrimSpace(string(pinBytes))
	for _, backend := range prepared {
		backend.app.Conf.GetMap("workload")["root_sha256"] = rootPin
	}

	t.Cleanup(func() { _ = gatewayApp.ShutdownWithTimeout(3 * time.Second) })
	if e = gatewayApp.EnableEtcdDiscovery(nil); e != nil {
		t.Fatal(e)
	}
	gw, e := NewEtcdGateway(gatewayApp)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = gw.Close() })
	startBackend := func(service, id, state string, healthService bool) (*core.Core, string) {
		t.Helper()
		item := prepared[nextBackend]
		nextBackend++
		app, address := item.app, item.address
		if e := app.EnableWorkloadIdentity(); e != nil {
			t.Fatal(e)
		}
		cachedRoot, readError := os.ReadFile(filepath.Join(state, "root.pem"))
		if readError != nil {
			t.Fatalf("后端未自动缓存独立根证书: %v", readError)
		}
		if len(cachedRoot) == 0 {
			t.Fatal("后端根证书缓存为空")
		}

		app.EnableGRPC(address)
		if healthService {
			app.RegisterGRPCService(func(server *grpc.Server) {
				h := health.NewServer()
				h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
				healthpb.RegisterHealthServer(server, h)
			})
		}
		if e := app.EnableEtcdRegistry(nil); e != nil {
			t.Fatal(e)
		}
		workloadTestServe(t, app)
		return app, address
	}
	echo, _ := startBackend("echo", "one", echoState, true)
	caller, _ := startBackend("caller", "one", filepath.Join(base, "caller-identity"), false)
	callDirect := func() {
		t.Helper()
		connection, e := caller.GrpcClient("echo")
		if e != nil {
			t.Fatal(e)
		}
		defer connection.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		response, e := healthpb.NewHealthClient(connection).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
		if e != nil || response.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("后端自动 mTLS 直连失败: %v %v", response, e)
		}
	}
	callDirect()
	checkHTTP := func() {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		var status int
		var body string
		for time.Now().Before(deadline) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/grpc/health/v1/health/check", bytes.NewBufferString(`{}`))
			request.Header.Set("Content-Type", "application/json")
			gatewayApp.ServeHTTP(recorder, request)
			status = recorder.Code
			body = recorder.Body.String()
			if status == 200 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("Gateway 自动 reflection 路由未成功: %d %s", status, body)
	}
	checkHTTP()
	// 同名副本拥有独立私钥，地址变更不要求 Gateway 增加目标凭据配置。
	replica, _ := startBackend("echo", "two", filepath.Join(base, "echo-replica"), true)
	callDirect()
	checkHTTP()
	if e = replica.ShutdownWithTimeout(3 * time.Second); e != nil {
		t.Fatal(e)
	}
	identityFile := filepath.Join(echoState, "identity.json")
	before, e := os.ReadFile(identityFile)
	if e != nil {
		t.Fatal(e)
	}
	if e = echo.ShutdownWithTimeout(3 * time.Second); e != nil {
		t.Fatal(e)
	}
	restarted, _ := startBackend("echo", "one", echoState, true)
	after, e := os.ReadFile(identityFile)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("有效缓存重启不应重新颁发证书")
	}
	callDirect()
	checkHTTP()
	// 签发器停机不影响已缓存后端直接通信；Gateway 数据面由其它测试覆盖。
	if e = gatewayApp.ShutdownWithTimeout(3 * time.Second); e != nil {
		t.Fatal(e)
	}
	callDirect()
	if e = restarted.ShutdownWithTimeout(3 * time.Second); e != nil {
		t.Fatal(e)
	}
	_, _ = startBackend("echo", "one", echoState, true)
	callDirect()
}
