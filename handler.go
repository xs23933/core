package core

import "fmt"

type handler interface {
	Core(c ...*Core) *Core
	Init()
	PushHandler(method, path string)
	HandName(name ...string) string
	Prefix(prefix ...string) string
	Preload(c *Ctx)
}

// HandlerFunc defines the handlerFunc
type HandlerFunc func(*Ctx)

type HandlerFuncs []HandlerFunc

// Handler base Handler
type Handler struct {
	core     *Core
	Handlers map[string]struct{} // Record callable handle
	name     string
	prefix   string
}

// Core get or set Core
func (h *Handler) Core(c ...*Core) *Core {
	if len(c) > 0 { // set core
		h.core = c[0]
		h.Handlers = make(map[string]struct{})
	}
	return h.core
}

func (h *Handler) HandName(name ...string) string {
	if len(name) > 0 {
		h.name = name[0]
	}
	return h.name
}

// PushHandler push list to PushHandler
func (h *Handler) PushHandler(method, path string) {
	h.Handlers[fmt.Sprintf("%s|%s", method, path)] = struct{}{}
}

// Preload do nothing wait children rewrite any request before call this func
func (h *Handler) Preload(c *Ctx) {}

// Init do nothing wait children rewrite app start call this func
func (h *Handler) Init() {}

// Prefix set or get prefix e.g h.Prefix("/api")
func (h *Handler) Prefix(prefix ...string) string {
	if len(prefix) > 0 {
		h.prefix = prefix[0]
	}
	return h.prefix
}
