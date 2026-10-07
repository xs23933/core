package core

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/xs23933/core/v3/etcd"
)

func TestEnableEtcdRegistrySerializesConcurrentReplacement(t *testing.T) {
	endpoint := startAutomaticHTTPRouteEtcd(t)
	app := New(Options{"debug": false})
	t.Cleanup(app.shutdown)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondCandidate := make(chan struct{}, 1)
	var creations atomic.Int32
	originalNew := newCoreEtcdRegistry
	originalRegister := registerCoreEtcdRegistry
	newCoreEtcdRegistry = func(options *etcd.Options) (*etcd.Registry, error) {
		if creations.Add(1) == 2 {
			secondCandidate <- struct{}{}
		}
		return originalNew(options)
	}
	var registers atomic.Int32
	registerCoreEtcdRegistry = func(registry *etcd.Registry) error {
		if registers.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		return originalRegister(registry)
	}
	t.Cleanup(func() { newCoreEtcdRegistry = originalNew; registerCoreEtcdRegistry = originalRegister })
	base := etcd.Options{Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second, Namespace: "serial", ServiceName: "caller", ServiceID: "same", ServiceAddr: "127.0.0.1:18080", TTL: 5}
	first, second := base, base
	first.Version = "first"
	second.Version = "second"
	results := make(chan error, 2)
	go func() { results <- app.EnableEtcdRegistry(&first) }()
	<-firstEntered
	secondStarted := make(chan struct{})
	go func() { close(secondStarted); results <- app.EnableEtcdRegistry(&second) }()
	<-secondStarted
	premature := false
	select {
	case <-secondCandidate:
		premature = true
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseFirst)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Errorf("registration: %v", err)
		}
	}
	if premature {
		t.Fatal("second candidate started before first replacement completed")
	}
	if err := app.EtcdDiscovery.Watch("caller"); err != nil {
		t.Fatal(err)
	}
	instances := app.EtcdDiscovery.GetServices("caller")
	if len(instances) != 1 || instances[0].Version != "second" {
		t.Fatalf("active candidate=%+v", instances)
	}
}
