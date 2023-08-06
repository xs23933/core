package core

import (
	"errors"
	"fmt"
	"net/http"
)

type ErrorHandler func(Ctx, error) error

type HandlerFunc = func(Ctx) error
type HandlerFuncs []HandlerFunc
type HandlerFun = func(Ctx)
type HandlerNormal = func(w http.ResponseWriter, r *http.Request)

func DefaultErrorHandler(c Ctx, err error) error {
	code := StatusInternalServerError
	var e *Error
	if errors.As(err, &e) {
		code = e.Code
	}
	c.SetHeader(HeaderContentType, MIMETextPlainCharsetUTF8)
	return c.SendStatus(code, err.Error())
}

type handler interface {
	Core(app ...*Core) *Core
	Preload(Ctx) error
	HandName(name ...string) string
	Prefix(prefix ...string) string
	PushHandler(method, path string)
	Init()
}

type Handler struct {
	app      *Core
	Handlers map[string]any // record callable handle
	name     string
	prefix   string
	ID       string
}

func (h *Handler) Core(app ...*Core) *Core {
	if len(app) > 0 {
		h.app = app[0]
		h.Handlers = make(map[string]any)
	}
	return h.app
}

func (h *Handler) HandName(name ...string) string {
	if len(name) > 0 {
		h.name = name[0]
		D("AutoRoute %s", h.name)
	}
	return h.name
}

func (h *Handler) PushHandler(method, path string) {
	h.Handlers[fmt.Sprintf("%s|%s", method, path)] = struct{}{}
}

func (h *Handler) Preload(c Ctx) error {
	return c.Next()
}

func (h *Handler) Init() {}

func (h *Handler) Prefix(prefix ...string) string {
	if len(prefix) > 0 {
		h.prefix = prefix[0]
	}
	return h.prefix
}
