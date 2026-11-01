package gateway

import (
	"sync"
	"testing"
	"time"
)

func TestGatewayCloseRejectsLateFallbackRegistration(t *testing.T) {
	gw := newLifecycleTestGateway(nil)
	t.Cleanup(func() {
		_ = gw.Close()
		if err := gw.app.ShutdownWithTimeout(time.Second); err != nil {
			t.Error(err)
		}
	})
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	gw.addDesiredServiceInstance(registeredServiceInstance{ServiceName: "late", InstanceID: "one", GRPCAddr: "127.0.0.1:1"})
	if len(gw.loadDesiredServiceSnapshot()) != 0 || len(gw.connPool.ServiceNames()) != 0 {
		t.Fatal("request fallback resurrected closed Gateway state")
	}
}
func TestGatewayConcurrentTaskAdmissionAndClose(t *testing.T) {
	gw := newLifecycleTestGateway(nil)
	t.Cleanup(func() {
		_ = gw.Close()
		if err := gw.app.ShutdownWithTimeout(time.Second); err != nil {
			t.Error(err)
		}
	})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				gw.launchTask(func() {})
			}
		}()
	}
	close(start)
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if gw.launchTask(func() {}) {
		t.Fatal("closed Gateway admitted task")
	}
}
