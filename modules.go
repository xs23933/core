package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Engine struct {
	*Core
	quit chan os.Signal
}

type canExitModule interface {
	Exit(*sync.WaitGroup) error
}

type canStartModule interface {
	Start(*Engine)
}

type hasHandler interface {
	Preload(*Ctx)
}

func NewEngine(conf ...Options) *Engine {
	engine := &Engine{
		Core: Default(conf...),
		quit: make(chan os.Signal, 1),
	}
	signal.Notify(engine.quit, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go engine.init()
	return engine
}

func (e *Engine) loadMods() {
	for _, m := range GetModules("module") {
		mo := m.Instance()
		if mod, ok := mo.(canStartModule); ok {
			go mod.Start(e)
		}
		if mod, ok := mo.(hasHandler); ok {
			e.Core.Use(mod)
		}
	}
}

func (e *Engine) ListenAndServe(addr ...string) error {
	defer e.Exit()
	e.loadMods()
	err := e.Core.ListenAndServe(addr...)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (e *Engine) Serve(ln net.Listener) error {
	defer e.Exit()
	e.loadMods()
	err := e.Core.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (e *Engine) init() {
	for range e.quit {
		ctx, _ := context.WithTimeout(context.Background(), 3*time.Second)
		_ = e.Core.srv.Shutdown(ctx)
	}
}

func (e *Engine) Shutdown() {
	e.quit <- os.Interrupt
}

func (e *Engine) Exit() {
	defer func() {
		if r := recover(); r != nil {
			Erro("exit module %v", r)
		}
	}()
	modulesMu.Lock()
	defer modulesMu.Unlock()
	wg := sync.WaitGroup{}
	for _, m := range modules {
		pos := m.Instance()
		if mod, ok := pos.(canExitModule); ok {
			wg.Add(1)
			go mod.Exit(&wg)
		}
	}
	wg.Wait()
}

type Module interface {
	Module() ModuleInfo
}

type ModuleInfo struct {
	ID       string
	Instance func() Module
}

func RegisterModule(inst Module) {
	mod := inst.Module()
	modulesMu.Lock()
	defer modulesMu.Unlock()
	if _, ok := modules[mod.ID]; ok {
		Log("module already registered: %s\n", mod.ID)
		return
	}
	modules[mod.ID] = mod
}

func GetModule(id string) (ModuleInfo, error) {
	modulesMu.RLock()
	defer modulesMu.RUnlock()
	m, ok := modules[id]
	if !ok {
		return ModuleInfo{}, fmt.Errorf("module not register: %s", id)
	}
	return m, nil
}

func GetModules(scope string) []ModuleInfo {
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

var (
	modules   = make(map[string]ModuleInfo)
	modulesMu sync.RWMutex
)
