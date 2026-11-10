package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/etcd/server/v3/embed"
)

// These tests compile and run the emitted source, not just the generator binary.
func TestGeneratedProjects(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"http", "grpc"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "grpc" {
				for _, tool := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
					if _, err := exec.LookPath(tool); err != nil {
						t.Skipf("gRPC generation needs %s; CI installs it", tool)
					}
				}
			}
			cfg := config(t)
			cfg.ProjectType = kind
			if err := CreateProject(&cfg); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(cfg.OutputDir, cfg.ProjectName)
			env := append(os.Environ(), "GOWORK=off")
			if kind == "grpc" {
				env = append(env, "CORECTL_TEST_ETCD="+startEtcd(t))
			}
			run := func(name string, args ...string) {
				t.Helper()
				cmd := exec.Command(name, args...)
				cmd.Dir = dir
				cmd.Env = env
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s %v: %v\n%s", name, args, err, output)
				}
			}
			if kind == "grpc" {
				run("protoc", "--go_out=.", "--go_opt=module="+cfg.ModulePath, "--go-grpc_out=.", "--go-grpc_opt=module="+cfg.ModulePath, "proto/user/v1/user.proto")
			}
			// Source validation is isolated from the release smoke check documented in CI.
			run("go", "mod", "edit", "-replace=github.com/xs23933/core/v3="+repo)
			run("go", "mod", "tidy")
			run("go", "test", "./...", "-count=1", "-timeout=60s")
			run("go", "vet", "./...")
			run("go", "build", "-o", filepath.Join(dir, "server"), "./cmd")
		})
	}
}

func startEtcd(t *testing.T) string {
	t.Helper()
	reserve := func() url.URL {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return url.URL{Scheme: "http", Host: address}
	}
	cfg := embed.NewConfig()
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "error"
	client, peer := reserve(), reserve()
	cfg.ListenClientUrls = []url.URL{client}
	cfg.AdvertiseClientUrls = []url.URL{client}
	cfg.ListenPeerUrls = []url.URL{peer}
	cfg.AdvertisePeerUrls = []url.URL{peer}
	cfg.InitialCluster = fmt.Sprintf("%s=%s", cfg.Name, peer.String())
	server, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		t.Fatal("etcd not ready")
	}
	return "http://" + server.Clients[0].Addr().String()
}
