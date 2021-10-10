package core

import (
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

type Core struct {
	tree   *tree
	pool   sync.Pool
	addr   string
	Debug  bool
	Conf   config
	assets config

	// Value of 'maxMemory' param that is given to http.Request's ParseMultipartForm
	// method call.
	MaxMultipartMemory int64

	ViewFuncMap     template.FuncMap
	RemoteIPHeaders []string
}

func (c *Core) assignCtx(w http.ResponseWriter, r *http.Request) *Ctx {
	ctx := c.pool.Get().(*Ctx)
	ctx.init(w, r, c)
	return ctx
}

func (c *Core) releaseCtx(ctx *Ctx) {
	ctx.Context = nil
	c.pool.Put(ctx)
}

func (c *Core) Use(args ...interface{}) *Core {
	path := ""
	handlers := make([]interface{}, 0)
	for _, arg := range args {
		switch a := arg.(type) {
		case string:
			path = a
		case func(*Ctx), HandlerFunc, func(http.ResponseWriter, *http.Request), http.Handler:
			handlers = append(handlers, a)
		case handler:
			c.buildHanders(a)
			goto done
		default:
			log.Fatal(ErrHandleNotSupport)
		}
	}
	c.AddHandle(MethodUse, path, handlers)
done:
	return c
}

func (c *Core) buildHanders(h handler) {
	h.Core(c)
	h.Init()
	// register routers
	refCtl := reflect.TypeOf(h)
	h.HandName(refCtl.Elem().String())
	methodCount := refCtl.NumMethod()
	valFn := reflect.ValueOf(h)
	prefix := h.Prefix()
	if prefix == "" {
		prefix = "/"
	}
	c.AddHandle(MethodUse, prefix, h.Preload) // Register global preload
	for i := 0; i < methodCount; i++ {
		m := refCtl.Method(i)
		name := toNamer(m.Name)
		switch fn := (valFn.Method(i).Interface()).(type) {
		case func(*Ctx), HandlerFunc, func(http.ResponseWriter, *http.Request), http.Handler:
			for _, method := range Methods {
				if strings.HasPrefix(name, strings.ToLower(method)) {
					name = fixURI(prefix, name, method)
					c.AddHandle(method, name, fn)
					h.PushHandler(method, name)
				}
			}
		}
	}
}

// Get add get method
//
//  path string /foo
//  handler core.Handle || http.HandlerFunc || http.Handler
//
//  > add method
//
//  c.Get("/foo", func(c *core.Ctx){
//		c.SendString("Hello world")
//  })
func (c *Core) Get(path string, handler interface{}) error {
	return c.AddHandle(MethodGet, path, handler)
}

// Post add get method
//
//  > see Get
func (c *Core) Post(path string, handler interface{}) error {
	return c.AddHandle(MethodPost, path, handler)
}

// Head add get method
//
//  > see Get
func (c *Core) Head(path string, handler interface{}) error {
	return c.AddHandle(MethodHead, path, handler)
}

// Put add get method
//
//  > see Get
func (c *Core) Put(path string, handler interface{}) error {
	return c.AddHandle(MethodPut, path, handler)
}

// Delete add get method
//
//  > see Get
func (c *Core) Delete(path string, handler interface{}) error {
	return c.AddHandle(MethodDelete, path, handler)
}

// Connect add get method
//
//  > see Get
func (c *Core) Connect(path string, handler interface{}) error {
	return c.AddHandle(MethodConnect, path, handler)
}

// Options add get method
//
//  > see Get
func (c *Core) Options(path string, handler interface{}) error {
	return c.AddHandle(MethodOptions, path, handler)
}

// Trace add get method
//
//  > see Get
func (c *Core) Trace(path string, handler interface{}) error {
	return c.AddHandle(MethodTrace, path, handler)
}

// Patch add get method
//
//  > see Get
func (c *Core) Patch(path string, handler interface{}) error {
	return c.AddHandle(MethodPatch, path, handler)
}

func (c *Core) Static(path, dirname string) error {
	c.assets[path] = dirname
	return nil
}

func (c *Core) static(w http.ResponseWriter, r *http.Request, path, dir string) {
	file := filepath.Join(dir, r.URL.Path)
	http.ServeFile(w, r, file)
}

// AddHandle
//
//  methods string || []string
//  path string /foo
//  handler core.Handle || http.HandlerFunc || http.Handler
//
//  app.AddHandle("GET", "/foo", func(c*core.Ctx){
// 		c.SendString("hello world")
//  })
func (c *Core) AddHandle(methods interface{}, path string, handler interface{}) error {
	var handle HandlerFunc
	switch v := handler.(type) {
	case []interface{}:
		for _, h := range v {
			if err := c.AddHandle(methods, path, h); err != nil {
				return err
			}
		}
		return nil
	case HandlerFunc:
		handle = v
	case func(*Ctx):
		handle = HandlerFunc(v)
	case func(http.ResponseWriter, *http.Request):
		handle = HandlerFunc(func(c *Ctx) { v(c.w, c.r) })
	case http.Handler:
		handle = HandlerFunc(func(c *Ctx) { v.ServeHTTP(c.w, c.r) })
	default:
		return ErrHandleNotSupport
	}
	if path == "" {
		path = "/"
	}
	if c.Debug {
		D("%v: %s", methods, path)
	}
	switch v := methods.(type) {
	case string:
		return c.tree.Insert([]string{v}, path, handle)
	case []string:
		return c.tree.Insert(v, path, handle)
	}
	return ErrMethodNotAllowed
}

func (c *Core) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for k, v := range c.assets {
		if strings.HasPrefix(r.URL.Path, k) {
			c.static(w, r, k, v.(string))
			return
		}
	}

	ctx := c.assignCtx(w, r)
	defer c.releaseCtx(ctx)
	st := time.Now()
	result, err := c.tree.Find(r.Method, r.URL.Path)
	if err == nil {
		ctx.params = result.params
		ctx.handlers = append(result.preloads, result.handler...)
		ctx.Next()
		ctx.SetHeader(HeaderTk, time.Since(st).String())
		ctx.wm.DoWriteHeader()
		return
	}
	requestLog(StatusNotFound, ctx.Method(), ctx.Path(), time.Since(st).String())
	ctx.SendStatus(http.StatusNotFound, err.Error())
}

// New New Core
func New(conf ...config) *Core {
	c := &Core{
		tree:            NewTree(),
		assets:          make(config),
		ViewFuncMap:     template.FuncMap{},
		RemoteIPHeaders: []string{"X-Forwarded-For", "X-Real-IP"},
		pool: sync.Pool{
			New: func() interface{} {
				return &Ctx{
					wm: resp{},
				}
			},
		},
	}
	if len(conf) > 0 {
		c.Conf = conf[0]
		c.Debug = c.Conf.GetBool("debug")
		c.addr = c.Conf.ToString("listen")
		c.assets = c.Conf.GetMap("static")
		c.MaxMultipartMemory = c.Conf.GetInt64("maxMultipartMemory", defaultMultipartMemory)
	}
	return c
}

// Default init and use Logger And Recovery
func Default(conf ...config) *Core {
	c := New(conf...)
	out := ioutil.Discard
	if c.Debug {
		out = os.Stdout
	}
	c.Use(Logger(LoggerConfig{ForceColor: c.Debug, Output: out}), Recovery())
	if conf := c.Conf.GetMap("database"); conf != nil {
		NewModel(conf, c.Debug)
	}
	return c
}

func (c *Core) ListenAndServe(addr ...string) error {
	if len(addr) > 0 {
		c.addr = addr[0]
	}
	if c.addr == "" {
		c.addr = ":8080"
	}
	if !strings.Contains(c.addr, ":") {
		c.addr = fmt.Sprintf(":%s", c.addr)
	}
	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		return err
	}
	return c.Serve(ln)
}

func (c *Core) Serve(ln net.Listener) error {
	Log("Listen %s\n", strings.TrimPrefix(ln.Addr().String(), "[::]"))
	return http.Serve(ln, c)
}

// SetFuncMap sets the FuncMap used for template.FuncMap.
func (c *Core) SetFuncMap(funcMap template.FuncMap) *Core {
	c.ViewFuncMap = funcMap
	return c
}
