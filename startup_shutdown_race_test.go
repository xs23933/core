package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

type limitedStopHandler struct {
	Handler
	entered, exit, initialized chan struct{}
	stopOnce                   sync.Once
}

func (h *limitedStopHandler) Init()             { h.Prefix("/limited-stop"); close(h.initialized) }
func (h *limitedStopHandler) Start(*Core) error { close(h.entered); <-h.exit; return nil }
func (h *limitedStopHandler) Stop(*Core) error  { h.stopOnce.Do(func() { close(h.exit) }); return nil }

func TestLimitedErrGroupShutdownReleasesStopOnlyModules(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			app := New(Options{"debug": false})
			app.ErrGroup().SetLimit(1)
			instances := make([]Module, 0, count)
			handlers := make([]*limitedStopHandler, 0, count)
			for i := 0; i < count; i++ {
				h := &limitedStopHandler{entered: make(chan struct{}), exit: make(chan struct{}), initialized: make(chan struct{})}
				handlers = append(handlers, h)
				instances = append(instances, h)
				defer h.Stop(app)
			}
			registerStartupCompatibilityModules(t, app, instances...)
			result := make(chan error, 1)
			go func() { result <- app.prepareServe(nil) }()
			<-handlers[0].entered
			<-handlers[0].initialized
			// One module fills the group before its shutdown observer; two modules also
			// exercise a queued module Start. Stop is the sole exit signal here.
			if err := app.ShutdownWithTimeout(time.Second); err != nil {
				t.Errorf("shutdown with limited ErrGroup=%v", err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("startup=%v", err)
				}
			case <-time.After(time.Second):
				t.Error("startup admission did not observe shutdown")
			}
		})
	}
}

func TestLimitedErrGroupStillWaitsForFiniteStart(t *testing.T) {
	app := New(Options{"debug": false})
	app.ErrGroup().SetLimit(1)
	first := &limitedStopHandler{entered: make(chan struct{}), exit: make(chan struct{}), initialized: make(chan struct{})}
	second := &limitedStopHandler{entered: make(chan struct{}), exit: make(chan struct{}), initialized: make(chan struct{})}
	defer first.Stop(app)
	defer second.Stop(app)
	registerStartupCompatibilityModules(t, app, first, second)
	result := make(chan error, 1)
	go func() { result <- app.prepareServe(nil) }()
	<-first.entered
	first.Stop(app)
	select {
	case <-second.entered:
	case <-time.After(time.Second):
		t.Fatal("second Start did not acquire freed capacity")
	}
	second.Stop(app)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("finite Starts did not permit readiness")
	}
	if !app.ServingReady() {
		t.Fatal("finite Start configuration failed")
	}
}

func TestCanceledGRPCAdmissionClosesListener(t *testing.T) {
	app := New(Options{"debug": false})
	defer app.ShutdownWithTimeout(time.Second)
	app.ErrGroup().SetLimit(0)
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	app.EnableGRPC(addr)
	app.GetGRPCServer()
	app.stop()
	if err := app.startGRPCServer(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission=%v", err)
	}
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("unadmitted server retained TCP listener: %v", err)
	}
	probe.Close()
}

func TestLateHTTP3PublicationClosesPacketConn(t *testing.T) {
	app := New(Options{"debug": false})
	if err := app.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := app.serveHTTP3(conn, nil); err != nil {
		t.Fatal(err)
	}
	if app.h3 != nil {
		t.Fatal("late callback published HTTP3 after shutdown")
	}
	if _, err := conn.WriteTo([]byte("test"), conn.LocalAddr()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("late packet conn not closed: %v", err)
	}
}

func TestHTTP3PublicationAndShutdownShareOwnership(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsServer.Close()
	app := New(Options{"debug": false})
	defer app.ShutdownWithTimeout(time.Second)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() { done <- app.serveHTTP3(conn, tlsServer.TLS) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		app.mutex.Lock()
		published := app.h3 != nil
		app.mutex.Unlock()
		if published {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("HTTP3 exited before publication: %v", err)
		case <-deadline.C:
			t.Fatal("HTTP3 not published")
		case <-poll.C:
		}
	}
	if err := app.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP3 remained serving after shutdown")
	}
	if _, err := conn.WriteTo([]byte("test"), conn.LocalAddr()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("HTTP3 packet conn retained: %v", err)
	}
}
