package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServingReadinessWaitsForSuccessfulPreparation(t *testing.T) {
	old := markCoreEtcdRegistryReadyForServe
	t.Cleanup(func() { markCoreEtcdRegistryReadyForServe = old })
	markCoreEtcdRegistryReadyForServe = func(*Core, context.Context) error { return errors.New("registry unavailable") }
	failed := New()
	if failed.ServingReady() {
		t.Fatal("new application ready")
	}
	if err := failed.prepareServeOnce(); err == nil {
		t.Fatal("preparation accepted registry failure")
	}
	if failed.ServingReady() {
		t.Fatal("failed preparation ready")
	}
	markCoreEtcdRegistryReadyForServe = func(*Core, context.Context) error { return nil }
	app := New()
	if err := app.prepareServeOnce(); err != nil {
		t.Fatal(err)
	}
	if !app.ServingReady() {
		t.Fatal("successful preparation not ready")
	}
	if err := app.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	if app.ServingReady() {
		t.Fatal("stopped application ready")
	}
	_ = failed.ShutdownWithTimeout(time.Second)
	_ = failed.eg.Wait()
	_ = app.eg.Wait()
}
