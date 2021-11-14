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

	"golang.org/x/sync/errgroup"
)

type Engine struct {
	*Core
	stop context.CancelFunc
	Ctx  context.Context
	EG   *errgroup.Group
}

type canStartModule interface {
	Start(*Engine) error
}

type hasHandler interface {
	Preload(*Ctx)
}

func NewEngine(conf ...Options) *Engine {
	engine := &Engine{
		Core: Default(conf...),
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine.EG, engine.Ctx = errgroup.WithContext(ctx)
	engine.stop = cancel
	//创建监听退出chan
	c := make(chan os.Signal)
	//监听指定信号 ctrl+c kill
	const SIGUSR2 = syscall.Signal(0x1f)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM,
		syscall.SIGQUIT, syscall.SIGUSR1, SIGUSR2)
	go func() {
		for range c {
			cancel()
			engine.Server.Shutdown(ctx)
		}
	}()
	return engine
}

func (e *Engine) loadMods() {
	for _, m := range GetModules("module") {
		mo := m.Instance()
		if mod, ok := mo.(canStartModule); ok {
			e.EG.Go(func() error {
				return mod.Start(e)
			})
		}
		if mod, ok := mo.(hasHandler); ok {
			e.Core.Use(mod)
		}
	}
}

func (e *Engine) ListenAndServe(addr ...string) error {
	e.loadMods()
	err := e.Core.ListenAndServe(addr...)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (e *Engine) Serve(ln net.Listener) error {
	e.loadMods()
	err := e.Core.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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
