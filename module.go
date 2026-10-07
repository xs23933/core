package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

func (app *Core) addHandler(h handler) {
	h.Core(app)
	h.Init()
	refCtl := reflect.TypeOf(h)
	h.HandName(refCtl.Elem().String())
	methodCount := refCtl.NumMethod()
	valFn := reflect.ValueOf(h)
	prefix := h.Prefix()
	if prefix == "" {
		prefix = "/"
	}
	group := app.Group(prefix, app.ProcessedHandler(h.Preload)).(*Group)
	for i := range methodCount {
		m := refCtl.Method(i)
		name := ToNamer(m.Name)
		switch fn := (valFn.Method(i).Interface()).(type) {

		case HandlerFunc, HandlerFun, HandlerFuncs:
			for _, method := range app.RequestMethods {
				if strings.HasPrefix(name, strings.ToLower(method)) {
					name = FixURI(prefix, name, method)
					group.core().AddHandle([]string{method}, name, group, nil, app.ProcessedHandler(fn)...)
					app.recordAutomaticHTTPRoute(automaticHTTPRoute{
						Method:  strings.ToUpper(method),
						Path:    name,
						Handler: h.HandName() + "." + m.Name,
					})
					D("route: %s %s > %s.%s", method, name, h.HandName(), m.Name)
					h.PushHandler(method, name)
				}
			}
		}
	}
}

type canStart interface {
	Start(*Core) error
}

type canShutdown interface {
	Stop(*Core) error
}

type Mod interface {
	Init()
}

func RegHandle(mods ...any) {
	modulesMu.Lock()
	defer modulesMu.Unlock()
	for _, inst := range mods {
		refCtl := reflect.TypeOf(inst)
		id := "mod." + refCtl.Elem().String()
		if _, ok := modules[id]; ok {
			Log("module already registered: %s\n", id)
			continue
		}
		modules[id] = ModuleInfo{
			ID: id,
			Instance: func() Module {
				return inst.(Module)
			},
		}
	}
}

func RegisterModule(inst module) {
	mod := inst.Module()
	modulesMu.Lock()
	defer modulesMu.Unlock()
	if _, ok := modules[mod.ID]; ok {
		Log("module already registered: %s\n", mod.ID)
		return
	}
	modules[mod.ID] = mod
}

func (app *Core) loadMods() {
	modPrefix := app.modName
	for _, m := range app.getModules(modPrefix) {
		mo := m.Instance()
		app.mutex.Lock()
		app.loadedModules = append(app.loadedModules, mo)
		app.mutex.Unlock()
		if mod, ok := mo.(canStart); ok {
			app.eg.Go(func() error {
				select {
				case <-app.Ctx.Done():
					return nil
				default:
				}
				return mod.Start(app)
			})
		}
		if mod, ok := mo.(handler); ok {
			app.addHandler(mod)
		}
	}
	app.eg.Go(func() error {
		<-app.Ctx.Done()
		app.shutdown()
		return nil
	})
}

func (app *Core) ErrGroup() *errgroup.Group {
	return app.eg
}

func (app *Core) shutdown() {
	timeout := app.Conf.GetDuration("shutdown_timeout", 10*time.Second)
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if err := app.ShutdownWithTimeout(timeout); err != nil {
		Warn("shutdown incomplete: %v", err)
	}
}

func (app *Core) shutdownWithContext(ctx context.Context) error {
	app.shutdownOnce.Do(func() {
		app.mutex.Lock()
		app.shutdownStarted = true
		app.shutdownDone = make(chan struct{})
		app.shutdownContext = ctx
		app.mutex.Unlock()
		go app.performShutdown(ctx)
	})
	app.mutex.Lock()
	done, startedContext := app.shutdownDone, app.shutdownContext
	app.mutex.Unlock()
	select {
	case <-done:
		app.mutex.Lock()
		err := app.shutdownErr
		app.mutex.Unlock()
		return err
	default:
	}
	select {
	case <-done:
		app.mutex.Lock()
		err := app.shutdownErr
		app.mutex.Unlock()
		return err
	case <-ctx.Done():
		if startedContext.Err() != nil {
			app.forceCloseTransports()
		}
		return ctx.Err()
	case <-startedContext.Done():
		app.forceCloseTransports()
		return startedContext.Err()
	}
}

func (app *Core) forceCloseTransports() {
	if app.Server != nil {
		_ = app.Server.Close()
	}
	app.forceStopGRPC()
	if app.h3 != nil {
		go func() { _ = app.h3.Close() }()
	}
}

func (app *Core) performShutdown(ctx context.Context) {
	defer close(app.shutdownDone)
	app.mutex.Lock()
	registryCleanup := app.etcdRegistryCleanup
	discoveryCleanup := app.etcdDiscoveryCleanup
	app.etcdRegistryCleanup = nil
	app.etcdDiscoveryCleanup = nil
	app.etcdRegistry = nil
	app.etcdRegistryServiceName = ""
	app.etcdRegistryNamespace = ""
	hooks := append([]func(){}, app.shutdownHooks...)
	loadedModules := append([]Module(nil), app.loadedModules...)
	app.mutex.Unlock()

	// Withdraw before closing business dependencies. The caller deadline closes
	// transports even when etcd or a legacy cleanup callback does not return.
	if registryCleanup != nil {
		registryCleanup()
	}
	if app.stop != nil {
		app.stop()
	}

	var drainErr error
	var drainMu sync.Mutex
	var drains sync.WaitGroup
	drains.Add(3)
	go func() {
		defer drains.Done()
		if app.Server != nil {
			if err := app.Server.Shutdown(ctx); err != nil {
				drainMu.Lock()
				drainErr = errors.Join(drainErr, err)
				drainMu.Unlock()
			}
		}
	}()
	go func() {
		defer drains.Done()
		app.shutdownGRPCContext(ctx)
	}()
	go func() {
		defer drains.Done()
		if app.h3 != nil {
			if err := app.h3.Shutdown(ctx); err != nil {
				drainMu.Lock()
				drainErr = errors.Join(drainErr, err)
				drainMu.Unlock()
			}
		}
	}()
	drains.Wait()
	if ctx.Err() != nil {
		app.forceCloseTransports()
		drainErr = errors.Join(drainErr, ctx.Err())
	}
	for _, hook := range hooks {
		hook()
	}
	for _, mo := range loadedModules {
		if mod, ok := mo.(canShutdown); ok {
			if err := mod.Stop(app); err != nil {
				drainErr = errors.Join(drainErr, err)
			}
		}
	}
	if discoveryCleanup != nil {
		discoveryCleanup()
	}
	drainErr = errors.Join(drainErr, ctx.Err())
	app.mutex.Lock()
	app.EtcdDiscovery = nil
	app.shutdownErr = drainErr
	app.mutex.Unlock()
}

func (app *Core) getModules(scope string) []ModuleInfo {
	modulesMu.RLock()
	defer modulesMu.RUnlock()
	scopeParts := strings.Split(scope, ".")
	if scope == "" {
		scopeParts = []string{}
	}
	mods := make([]ModuleInfo, 0)
iterateModules:
	for id, m := range modules {
		modParts := strings.Split(id, ".")
		if len(modParts) < len(scopeParts) {
			continue
		}
		for i := range scopeParts {
			if modParts[i] != scopeParts[i] {
				continue iterateModules
			}
		}
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool {
		return mods[i].ID < mods[j].ID
	})
	return mods
}

func (app *Core) GetModule(id string) (ModuleInfo, error) {
	modulesMu.RLock()
	defer modulesMu.RUnlock()
	m, ok := modules[id]
	if !ok {
		return ModuleInfo{}, fmt.Errorf("module not register: %s", id)
	}
	return m, nil
}

type module interface {
	Module() ModuleInfo
}

type Module interface {
	Init()
}
type ModuleInfo struct {
	ID       string
	Instance func() Module
}

var (
	modules   = make(map[string]ModuleInfo)
	modulesMu sync.RWMutex
)
