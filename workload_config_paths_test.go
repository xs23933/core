package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadWorkloadPathsRelativeToConfigurationFile(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "services", "backend")
	cwd := filepath.Join(root, "unrelated-cwd")
	for _, dir := range []string{configDir, cwd} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	t.Setenv("CORE_TEST_WORKLOAD_STATE", "./workload")
	yaml := []byte("workload:\n  enabled: true\n  state_dir: ${ENV:CORE_TEST_WORKLOAD_STATE}\n  trust_bundle_file: ./trust/root.pem\n  bootstrap_token_file: ./secrets/token\n  issuer_url: https://issuer.internal:9444\nworkload_issuer:\n  state_dir: ./issuer\n  policy_file: ./policy.json\n  pin_file: ./root.pin\nother:\n  state_dir: ./untouched\n")
	for _, extension := range []string{".yaml", ".dat"} {
		path := filepath.Join(configDir, "config"+extension)
		data := yaml
		if extension == ".dat" {
			var err error
			data, err = EncryptAESBytes(yaml)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(cwd, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range []string{path, relative} {
			conf := LoadConfigFile(input)
			checks := map[string]string{"workload.state_dir": filepath.Join(configDir, "workload"), "workload.trust_bundle_file": filepath.Join(configDir, "trust", "root.pem"), "workload.bootstrap_token_file": filepath.Join(configDir, "secrets", "token"), "workload_issuer.state_dir": filepath.Join(configDir, "issuer"), "workload_issuer.policy_file": filepath.Join(configDir, "policy.json"), "workload_issuer.pin_file": filepath.Join(configDir, "root.pin"), "workload.issuer_url": "https://issuer.internal:9444", "other.state_dir": "./untouched"}
			for key, want := range checks {
				if got := conf.GetString(key); got != want {
					t.Fatalf("%s via %s=%q want %q", key, input, got, want)
				}
			}
			if admin := conf.GetString("workload.state_dir") + "-admin"; admin != filepath.Join(configDir, "workload-admin") {
				t.Fatalf("admin state path=%q", admin)
			}
		}
	}
}

func TestWorkloadPathResolutionPreservesAbsoluteAndEmptyPaths(t *testing.T) {
	root := t.TempDir()
	absolute := filepath.Join(root, "absolute-state")
	conf := Options{"workload": Options{"state_dir": absolute, "trust_bundle_file": "", "bootstrap_token_file": ""}, "workload_issuer": map[string]any{"state_dir": absolute, "policy_file": "", "pin_file": ""}}
	resolveWorkloadConfigPaths(conf, filepath.Join(root, "config.yaml"))
	for _, key := range []string{"workload.state_dir", "workload_issuer.state_dir"} {
		if got := conf.GetString(key); got != absolute {
			t.Fatalf("absolute path changed: %s=%q", key, got)
		}
	}
	for _, key := range []string{"workload.trust_bundle_file", "workload.bootstrap_token_file", "workload_issuer.policy_file", "workload_issuer.pin_file"} {
		if got := conf.GetString(key); got != "" {
			t.Fatalf("empty path changed: %s=%q", key, got)
		}
	}
	programmatic := New(Options{"workload": Options{"state_dir": "./workload"}})
	if got := programmatic.Conf.GetString("workload.state_dir"); got != "./workload" {
		t.Fatalf("programmatic path changed: %q", got)
	}
}

func TestWorkloadColdStartFromDifferentWorkingDirectory(t *testing.T) {
	opts, root := managedTestIssuer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	url := "https://" + address
	opts["workload"].(Options)["issuer_url"] = url
	opts["workload_issuer"].(Options)["listen"] = address
	opts["workload_issuer"].(Options)["advertise_url"] = url
	gateway := New(opts)
	if err = gateway.EnableWorkloadIdentity(); err != nil {
		t.Fatal(err)
	}
	defer gateway.ShutdownWithTimeout(time.Second)
	block, _ := pem.Decode(gateway.workload.issuer.RootPEM())
	pin := sha256.Sum256(block.Bytes)
	configDir := filepath.Join(root, "deployment")
	cwd := filepath.Join(root, "separate-cwd")
	for _, dir := range []string{configDir, cwd} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	yaml := fmt.Sprintf("workload:\n  enabled: true\n  service_name: backend\n  trust_domain: test.local\n  issuer_url: %s\n  root_sha256: %s\n  bootstrap_token_file: %s\n  state_dir: ./workload\n", url, hex.EncodeToString(pin[:]), filepath.Join(root, "token"))
	path := filepath.Join(configDir, "config.yaml")
	if err = os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(configDir, "workload")
	if _, err = os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatal("cold start unexpectedly has identity directory")
	}
	t.Chdir(cwd)
	backend := New(LoadConfigFile(path))
	if err = backend.EnableWorkloadIdentity(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"root.pem", "identity.json", "lock"} {
		if _, err = os.Stat(filepath.Join(stateDir, file)); err != nil {
			t.Fatalf("cold start artifact %s: %v", file, err)
		}
	}
	if _, err = os.Stat(filepath.Join(cwd, "workload")); !os.IsNotExist(err) {
		t.Fatal("identity directory created under process cwd")
	}
	original, err := os.ReadFile(filepath.Join(stateDir, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	if err = gateway.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	cached := New(LoadConfigFile(path))
	if err = cached.EnableWorkloadIdentity(); err != nil {
		t.Fatalf("cached identity did not start offline: %v", err)
	}
	defer cached.ShutdownWithTimeout(time.Second)
	current, err := os.ReadFile(filepath.Join(stateDir, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatal("offline restart replaced existing identity")
	}
}
