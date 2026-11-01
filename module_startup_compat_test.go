package core

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// This legacy Init explicitly waits for a long-running Start. Bound the wait so
// an ordering regression fails locally instead of hanging the entire test suite.
type startupCompatibilityLegacyHandler struct {
	Handler
	started chan struct{}
	initErr error
	starts  atomic.Int32
	stops   atomic.Int32
}

func (h *startupCompatibilityLegacyHandler) Init() {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-h.started:
	case <-h.Core().Ctx.Done():
		h.initErr = h.Core().Ctx.Err()
	case <-timer.C:
		h.initErr = errors.New("legacy Init could not observe Start")
	}
	h.Prefix("/legacy-startup")
}
func (h *startupCompatibilityLegacyHandler) Start(app *Core) error {
	h.starts.Add(1)
	// Binding must precede Start, while Init is allowed to wait for this signal.
	if h.Core() != app {
		return errors.New("legacy Start ran before Core binding")
	}
	close(h.started)
	<-app.Ctx.Done()
	return nil
}
func (h *startupCompatibilityLegacyHandler) Stop(*Core) error { h.stops.Add(1); return nil }
func (h *startupCompatibilityLegacyHandler) Get(c Ctx) error  { return c.SendString("legacy") }

type startupCompatibilityPreparedHandler struct {
	Handler
	legacy      *startupCompatibilityLegacyHandler
	initialized bool
	failure     error
	started     chan struct{}
	starts      atomic.Int32
	stops       atomic.Int32
}

func (h *startupCompatibilityPreparedHandler) Init() {
	h.initialized = true
	h.Prefix("/prepared-startup")
}
func (h *startupCompatibilityPreparedHandler) Prepare(app *Core) error {
	if !h.initialized || h.Core() != app {
		return errors.New("Prepare ran before binding/Init")
	}
	if h.legacy.initErr != nil {
		return h.legacy.initErr
	}
	if app.ServingReady() {
		return errors.New("ready published before Prepare")
	}
	for _, path := range []string{"/legacy-startup", "/prepared-startup"} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			return fmt.Errorf("Prepare ran before route %s: %d", path, rec.Code)
		}
	}
	return h.failure
}
func (h *startupCompatibilityPreparedHandler) Start(app *Core) error {
	h.starts.Add(1)
	close(h.started)
	<-app.Ctx.Done()
	return nil
}
func (h *startupCompatibilityPreparedHandler) Stop(*Core) error { h.stops.Add(1); return nil }
func (h *startupCompatibilityPreparedHandler) Get(c Ctx) error  { return c.SendString("prepared") }

func registerStartupCompatibilityModules(t *testing.T, app *Core, instances ...Module) {
	t.Helper()
	app.modName = "startupcompatibility"
	modulesMu.Lock()
	for i, instance := range instances {
		id := fmt.Sprintf("%s.%d", app.modName, i)
		modules[id] = ModuleInfo{ID: id, Instance: func() Module { return instance }}
	}
	modulesMu.Unlock()
	t.Cleanup(func() {
		if err := app.ShutdownWithTimeout(time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- app.eg.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("module exit: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("startup tasks did not exit after cancellation")
		}
		modulesMu.Lock()
		defer modulesMu.Unlock()
		for i := range instances {
			delete(modules, fmt.Sprintf("%s.%d", app.modName, i))
		}
	})
}

func TestLegacyStartBeforeInitWithPrepareModules(t *testing.T) {
	for _, preparedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepared-first=%t", preparedFirst), func(t *testing.T) {
			app := New(Options{"debug": false})
			legacy := &startupCompatibilityLegacyHandler{started: make(chan struct{})}
			prepared := &startupCompatibilityPreparedHandler{legacy: legacy, started: make(chan struct{})}
			instances := []Module{legacy, prepared}
			if preparedFirst {
				instances[0], instances[1] = instances[1], instances[0]
			}
			registerStartupCompatibilityModules(t, app, instances...)
			if err := app.prepareServe(nil); err != nil {
				t.Fatal(err)
			}
			if legacy.initErr != nil || !app.ServingReady() {
				t.Fatalf("Init error=%v ready=%t", legacy.initErr, app.ServingReady())
			}
			select {
			case <-prepared.started:
			case <-time.After(time.Second):
				t.Fatal("prepared Start did not run")
			}
			if err := app.prepareServe(nil); err != nil {
				t.Fatal(err)
			}
			if legacy.starts.Load() != 1 || prepared.starts.Load() != 1 {
				t.Fatal("startup ran more than once")
			}
			if err := app.ShutdownWithTimeout(time.Second); err != nil {
				t.Fatal(err)
			}
			if legacy.stops.Load() != 1 || prepared.stops.Load() != 1 {
				t.Fatal("loaded modules were not stopped exactly once")
			}
		})
	}
}

func TestPrepareFailureCancelsAlreadyStartedLegacyHandler(t *testing.T) {
	app := New(Options{"debug": false})
	failure := errors.New("required preparation failed")
	legacy := &startupCompatibilityLegacyHandler{started: make(chan struct{})}
	prepared := &startupCompatibilityPreparedHandler{legacy: legacy, failure: failure, started: make(chan struct{})}
	registerStartupCompatibilityModules(t, app, legacy, prepared)
	if err := app.prepareServe(nil); !errors.Is(err, failure) {
		t.Fatalf("startup error=%v", err)
	}
	if legacy.initErr != nil || legacy.starts.Load() != 1 || prepared.starts.Load() != 0 || app.ServingReady() {
		t.Fatal("mixed startup failure violated lifecycle")
	}
	if app.Ctx.Err() == nil || legacy.stops.Load() != 1 || prepared.stops.Load() != 1 {
		t.Fatal("failure did not cancel/stop loaded modules")
	}
}
