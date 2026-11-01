package core

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
)

type shutdownPreparationHandler struct {
	Handler
	phase            string
	entered, release chan struct{}
	prepares, stops  atomic.Int32
}

func (h *shutdownPreparationHandler) Init() {
	h.Prefix("/shutdown-prepare")
	if h.phase == "init" {
		close(h.entered)
		<-h.Core().Ctx.Done()
		<-h.release
	}
}
func (h *shutdownPreparationHandler) Prepare(app *Core) error {
	h.prepares.Add(1)
	if h.phase == "prepare" {
		close(h.entered)
		<-app.Ctx.Done()
		<-h.release
	}
	return app.Ctx.Err()
}
func (h *shutdownPreparationHandler) Stop(*Core) error { h.stops.Add(1); return nil }

func TestShutdownWaitsForStartupBeforeReleasingDependencies(t *testing.T) {
	for _, phase := range []string{"init", "prepare"} {
		t.Run(phase, func(t *testing.T) {
			app := New(Options{"debug": false})
			h := &shutdownPreparationHandler{phase: phase, entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			defer once.Do(func() { close(h.release) })
			app.modName = "shutdownrace" + phase
			id := app.modName + ".handler"
			modulesMu.Lock()
			modules[id] = ModuleInfo{ID: id, Instance: func() Module { return h }}
			var lateConstructed atomic.Int32
			lateID := app.modName + ".late"
			modules[lateID] = ModuleInfo{ID: lateID, Instance: func() Module { lateConstructed.Add(1); return &Handler{} }}
			modulesMu.Unlock()
			t.Cleanup(func() {
				app.stop()
				// This case intentionally expires the original shutdown deadline.
				_ = app.ShutdownWithTimeout(time.Second)
				modulesMu.Lock()
				delete(modules, id)
				delete(modules, lateID)
				modulesMu.Unlock()
			})
			var cleaned atomic.Bool
			app.OnShutdown(func() { cleaned.Store(true) })
			started := make(chan error, 1)
			go func() { started <- app.prepareServe(nil) }()
			select {
			case <-h.entered:
			case <-time.After(time.Second):
				t.Fatal("startup did not enter hook")
			}
			err := app.ShutdownWithTimeout(30 * time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("shutdown with active startup=%v, want deadline", err)
			}
			if h.stops.Load() != 0 || cleaned.Load() {
				t.Error("Stop/dependencies released while startup hook still running")
			}
			once.Do(func() { close(h.release) })
			select {
			case err := <-started:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("startup=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("startup did not exit")
			}
			select {
			case <-app.shutdownDone:
			case <-time.After(time.Second):
				t.Fatal("background shutdown did not finish")
			}
			if app.ServingReady() || h.stops.Load() != 1 || !cleaned.Load() {
				t.Fatal("shutdown did not finalize once with ready=false")
			}
			if phase == "init" && (h.prepares.Load() != 0 || lateConstructed.Load() != 0) {
				t.Fatal("Prepare or later module factory invoked after shutdown")
			}
		})
	}
}

func TestShutdownCannotBeUndoneByLateRoutePublication(t *testing.T) {
	app := New(Options{"debug": false})
	app.modName = "late-publication-only"
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	previous := syncCoreAutomaticHTTPRoutesForServe
	syncCoreAutomaticHTTPRoutesForServe = func(*Core, context.Context) error { close(entered); <-release; return nil }
	defer func() { syncCoreAutomaticHTTPRoutesForServe = previous }()
	result := make(chan error, 1)
	go func() { result <- app.prepareServe(nil) }()
	<-entered
	err := app.ShutdownWithTimeout(30 * time.Millisecond)
	once.Do(func() { close(release) })
	startupErr := <-result
	if !errors.Is(startupErr, context.Canceled) || app.ServingReady() {
		t.Fatalf("late startup=%v ready=%v shutdown=%v", startupErr, app.ServingReady(), err)
	}
	<-app.shutdownDone
}

func TestEarlyForceStopDoesNotLeaveLateGRPCServerRunning(t *testing.T) {
	app := New(Options{"debug": false})
	defer app.ShutdownWithTimeout(time.Second)
	app.forceStopGRPC()
	server := app.GetGRPCServer()
	defer server.Stop()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	app.forceStopGRPC()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("early nil-server stop consumed shutdown for late gRPC server")
	}
}

type shutdownFinalizingStartHandler struct {
	Handler
	entered, canceled, release, exited chan struct{}
}

func (h *shutdownFinalizingStartHandler) Init() { h.Prefix("/finalizing-start") }
func (h *shutdownFinalizingStartHandler) Start(app *Core) error {
	close(h.entered)
	defer close(h.exited)
	<-app.Ctx.Done()
	close(h.canceled)
	<-h.release
	return nil
}
func (h *shutdownFinalizingStartHandler) Stop(*Core) error { return nil }

func TestShutdownKeepsDependenciesUntilModuleStartExits(t *testing.T) {
	resetModelState(t)
	app := New(Options{"debug": false, "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "startup.db")}})
	app.modName = "finalizingstart"
	h := &shutdownFinalizingStartHandler{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(h.release) })
	id := app.modName + ".handler"
	modulesMu.Lock()
	modules[id] = ModuleInfo{ID: id, Instance: func() Module { return h }}
	modulesMu.Unlock()
	t.Cleanup(func() {
		app.stop()
		_ = app.ShutdownWithTimeout(time.Second)
		modulesMu.Lock()
		delete(modules, id)
		modulesMu.Unlock()
	})
	db, err := app.Conn().DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.prepareServe(nil); err != nil {
		t.Fatal(err)
	}
	<-h.entered
	err = app.ShutdownWithTimeout(30 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown=%v, expected active Start to hold cleanup", err)
	}
	<-h.canceled
	if err := db.Ping(); err != nil {
		t.Errorf("database closed before module Start returned: %v", err)
	}
	once.Do(func() { close(h.release) })
	<-app.shutdownDone
	if err := db.Ping(); err == nil {
		t.Fatal("database remained open after module exit")
	}
}
