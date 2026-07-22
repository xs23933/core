package core

import (
	"errors"
	"reflect"
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

func TestApplyEtcdRegistryDefaultsAddsHTTPAddressWithoutMutatingCallerMetadata(t *testing.T) {
	app := New(Options{
		"debug": false,
		"etcd": map[string]any{
			"http_addr": "127.0.0.1:8080",
		},
	})
	callerMetadata := map[string]string{"region": "cn"}
	opts := &etcd.Options{Metadata: callerMetadata}

	app.applyEtcdRegistryDefaults(opts)

	if got := opts.Metadata["http_addr"]; got != "127.0.0.1:8080" {
		t.Fatalf("Metadata[http_addr] = %q, want configured address", got)
	}
	if !reflect.DeepEqual(callerMetadata, map[string]string{"region": "cn"}) {
		t.Fatalf("caller metadata mutated: %#v", callerMetadata)
	}
	opts.Metadata["region"] = "changed"
	if callerMetadata["region"] != "cn" {
		t.Fatal("registry metadata still aliases caller map")
	}
}

func TestApplyEtcdRegistryDefaultsPreservesExplicitHTTPAddressMetadata(t *testing.T) {
	app := New(Options{
		"debug": false,
		"etcd": map[string]any{
			"http_addr": "127.0.0.1:8080",
		},
	})
	opts := &etcd.Options{Metadata: map[string]string{"http_addr": "public.example:443"}}

	app.applyEtcdRegistryDefaults(opts)

	if got := opts.Metadata["http_addr"]; got != "public.example:443" {
		t.Fatalf("Metadata[http_addr] = %q, want explicit value", got)
	}
}

func TestApplyEtcdRegistryDefaultsWithoutHTTPAddressLeavesGRPCOnlyMetadataUnchanged(t *testing.T) {
	app := New(Options{"debug": false})
	opts := &etcd.Options{}

	app.applyEtcdRegistryDefaults(opts)

	if opts.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for gRPC-only registration", opts.Metadata)
	}
}

func TestEnableEtcdRegistryFirstFailureLeavesNoInstalledState(t *testing.T) {
	registry := new(etcd.Registry)
	discovery := new(etcd.Discovery)
	var deregisterCalls, closeDiscoveryCalls int
	restore := replaceCoreEtcdLifecycleFunctionsForTest(
		func(*etcd.Options) (*etcd.Registry, error) { return registry, nil },
		func(*etcd.Options) (*etcd.Discovery, error) { return discovery, nil },
		func(*etcd.Registry) error { return errors.New("register failed") },
		func(got *etcd.Registry) error {
			if got != registry {
				t.Fatalf("deregister registry = %p, want %p", got, registry)
			}
			deregisterCalls++
			return nil
		},
		func(got *etcd.Discovery) error {
			if got != discovery {
				t.Fatalf("close discovery = %p, want %p", got, discovery)
			}
			closeDiscoveryCalls++
			return nil
		},
	)
	t.Cleanup(restore)

	app := New(Options{"debug": false})
	err := app.EnableEtcdRegistry(&etcd.Options{ServiceName: "new", Namespace: "new-ns"})
	if err == nil || !errors.Is(err, errCoreEtcdRegisterForTest) {
		t.Fatalf("EnableEtcdRegistry error = %v, want register failure", err)
	}
	if app.etcdRegistry != nil || app.EtcdDiscovery != nil || app.etcdRegistryServiceName != "" || app.etcdRegistryNamespace != "" {
		t.Fatalf("failed registry installed partial state: registry=%p discovery=%p identity=%q/%q",
			app.etcdRegistry, app.EtcdDiscovery, app.etcdRegistryNamespace, app.etcdRegistryServiceName)
	}
	if deregisterCalls != 1 || closeDiscoveryCalls != 1 {
		t.Fatalf("failed resource cleanup = registry %d discovery %d, want 1/1", deregisterCalls, closeDiscoveryCalls)
	}
}

func TestEnableEtcdRegistryLaterFailurePreservesPriorSuccessfulState(t *testing.T) {
	registries := []*etcd.Registry{new(etcd.Registry), new(etcd.Registry)}
	discoveries := []*etcd.Discovery{new(etcd.Discovery), new(etcd.Discovery)}
	registryIndex := 0
	discoveryIndex := 0
	registerCalls := 0
	deregisterCalls := map[*etcd.Registry]int{}
	closeDiscoveryCalls := map[*etcd.Discovery]int{}
	restore := replaceCoreEtcdLifecycleFunctionsForTest(
		func(*etcd.Options) (*etcd.Registry, error) {
			value := registries[registryIndex]
			registryIndex++
			return value, nil
		},
		func(*etcd.Options) (*etcd.Discovery, error) {
			value := discoveries[discoveryIndex]
			discoveryIndex++
			return value, nil
		},
		func(*etcd.Registry) error {
			registerCalls++
			if registerCalls == 2 {
				return errors.New("register failed")
			}
			return nil
		},
		func(value *etcd.Registry) error {
			deregisterCalls[value]++
			return nil
		},
		func(value *etcd.Discovery) error {
			closeDiscoveryCalls[value]++
			return nil
		},
	)
	t.Cleanup(restore)

	app := New(Options{"debug": false})
	if err := app.EnableEtcdRegistry(&etcd.Options{ServiceName: "stable", Namespace: "stable-ns"}); err != nil {
		t.Fatalf("first EnableEtcdRegistry: %v", err)
	}
	err := app.EnableEtcdRegistry(&etcd.Options{ServiceName: "failed", Namespace: "failed-ns"})
	if err == nil || !errors.Is(err, errCoreEtcdRegisterForTest) {
		t.Fatalf("second EnableEtcdRegistry error = %v, want register failure", err)
	}
	if app.etcdRegistry != registries[0] || app.EtcdDiscovery != discoveries[0] {
		t.Fatalf("failed replacement changed active resources: registry=%p discovery=%p", app.etcdRegistry, app.EtcdDiscovery)
	}
	if app.etcdRegistryServiceName != "stable" || app.etcdRegistryNamespace != "stable-ns" {
		t.Fatalf("failed replacement changed identity to %q/%q", app.etcdRegistryNamespace, app.etcdRegistryServiceName)
	}
	serviceName, namespace, ok := app.EtcdRegistryIdentity()
	if !ok || serviceName != "stable" || namespace != "stable-ns" {
		t.Fatalf("public registry identity after failed replacement = %q/%q ok=%v, want stable/stable-ns true", serviceName, namespace, ok)
	}
	if deregisterCalls[registries[0]] != 0 || closeDiscoveryCalls[discoveries[0]] != 0 {
		t.Fatal("failed replacement closed the prior successful resources")
	}
	if deregisterCalls[registries[1]] != 1 || closeDiscoveryCalls[discoveries[1]] != 1 {
		t.Fatalf("failed replacement cleanup = registry %d discovery %d, want 1/1",
			deregisterCalls[registries[1]], closeDiscoveryCalls[discoveries[1]])
	}

	app.shutdown()
	app.shutdown()
	if deregisterCalls[registries[0]] != 1 || closeDiscoveryCalls[discoveries[0]] != 1 {
		t.Fatalf("active resource cleanup after repeated shutdown = registry %d discovery %d, want 1/1",
			deregisterCalls[registries[0]], closeDiscoveryCalls[discoveries[0]])
	}
}

func TestEnableEtcdRegistryDoesNotInstallCandidateAfterShutdown(t *testing.T) {
	registry := new(etcd.Registry)
	discovery := new(etcd.Discovery)
	registerStarted := make(chan struct{})
	allowRegister := make(chan struct{})
	var deregisterCalls, closeDiscoveryCalls int
	restore := replaceCoreEtcdLifecycleFunctionsForTest(
		func(*etcd.Options) (*etcd.Registry, error) { return registry, nil },
		func(*etcd.Options) (*etcd.Discovery, error) { return discovery, nil },
		func(*etcd.Registry) error {
			close(registerStarted)
			<-allowRegister
			return nil
		},
		func(got *etcd.Registry) error {
			if got != registry {
				t.Fatalf("deregister registry = %p, want %p", got, registry)
			}
			deregisterCalls++
			return nil
		},
		func(got *etcd.Discovery) error {
			if got != discovery {
				t.Fatalf("close discovery = %p, want %p", got, discovery)
			}
			closeDiscoveryCalls++
			return nil
		},
	)
	t.Cleanup(restore)

	app := New(Options{"debug": false})
	result := make(chan error, 1)
	go func() {
		result <- app.EnableEtcdRegistry(&etcd.Options{ServiceName: "late", Namespace: "late-ns"})
	}()
	<-registerStarted
	app.shutdown()
	close(allowRegister)

	if err := <-result; err == nil {
		t.Fatal("EnableEtcdRegistry error = nil after shutdown")
	}
	if app.etcdRegistry != nil || app.EtcdDiscovery != nil || app.etcdRegistryServiceName != "" || app.etcdRegistryNamespace != "" {
		t.Fatalf("shutdown race installed candidate: registry=%p discovery=%p identity=%q/%q",
			app.etcdRegistry, app.EtcdDiscovery, app.etcdRegistryNamespace, app.etcdRegistryServiceName)
	}
	if deregisterCalls != 1 || closeDiscoveryCalls != 1 {
		t.Fatalf("rejected candidate cleanup = registry %d discovery %d, want 1/1", deregisterCalls, closeDiscoveryCalls)
	}
}

var errCoreEtcdRegisterForTest = errors.New("register failed")

func replaceCoreEtcdLifecycleFunctionsForTest(
	newRegistry func(*etcd.Options) (*etcd.Registry, error),
	newDiscovery func(*etcd.Options) (*etcd.Discovery, error),
	register func(*etcd.Registry) error,
	deregister func(*etcd.Registry) error,
	closeDiscovery func(*etcd.Discovery) error,
) func() {
	oldNewRegistry := newCoreEtcdRegistry
	oldNewDiscovery := newCoreEtcdDiscovery
	oldRegister := registerCoreEtcdRegistry
	oldDeregister := deregisterCoreEtcdRegistry
	oldCloseDiscovery := closeCoreEtcdDiscovery
	newCoreEtcdRegistry = newRegistry
	newCoreEtcdDiscovery = newDiscovery
	registerCoreEtcdRegistry = func(registry *etcd.Registry) error {
		err := register(registry)
		if err != nil {
			return errCoreEtcdRegisterForTest
		}
		return nil
	}
	deregisterCoreEtcdRegistry = deregister
	closeCoreEtcdDiscovery = closeDiscovery
	return func() {
		newCoreEtcdRegistry = oldNewRegistry
		newCoreEtcdDiscovery = oldNewDiscovery
		registerCoreEtcdRegistry = oldRegister
		deregisterCoreEtcdRegistry = oldDeregister
		closeCoreEtcdDiscovery = oldCloseDiscovery
	}
}
