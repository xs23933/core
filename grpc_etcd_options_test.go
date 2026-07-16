package core

import (
	"testing"

	"github.com/xs23933/core/v3/etcd"
)

func TestApplyEtcdDiscoveryDefaultsPropagatesNamespace(t *testing.T) {
	app := New(Options{
		"etcd": map[string]any{
			"namespace": "xpay",
			"endpoints": []string{"127.0.0.1:2379"},
		},
	})
	opts := &etcd.Options{}

	app.applyEtcdDiscoveryDefaults(opts)

	if opts.Namespace != "xpay" {
		t.Fatalf("Namespace = %q, want xpay", opts.Namespace)
	}
	if len(opts.Endpoints) != 1 || opts.Endpoints[0] != "127.0.0.1:2379" {
		t.Fatalf("Endpoints = %#v, want configured endpoint", opts.Endpoints)
	}
}

func TestApplyEtcdRegistryDefaultsPreservesExplicitNamespace(t *testing.T) {
	app := New(Options{
		"etcd": map[string]any{
			"namespace":    "xpay",
			"service_name": "auth",
		},
	})
	opts := &etcd.Options{Namespace: "override"}

	app.applyEtcdRegistryDefaults(opts)

	if opts.Namespace != "override" {
		t.Fatalf("Namespace = %q, want explicit override", opts.Namespace)
	}
	if opts.ServiceName != "auth" {
		t.Fatalf("ServiceName = %q, want auth", opts.ServiceName)
	}
}
