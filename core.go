package core

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xs23933/core/reuseport"
	"golang.org/x/sync/errgroup"
)

type Core struct {
	*http.Server
	tree      *tree
	pool      sync.Pool
	addr      string
	Debug     bool
	Conf      Options
	assets    Options
	waiterMux sync.Mutex
	waiter    *errgroup.Group
	Views     Views

	// Value of 'maxMemory' param that is given to http.Request's ParseMultipartForm
	// method call.
	MaxMultipartMemory int64
	ViewFuncMap        template.FuncMap
	RemoteIPHeaders    []string
	Ln                 net.Listener
	NotFoundFunc       NotFoundFunc
	enablePrefork      bool
	networkProto       string
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
		case Views:
			c.Views = a
		case handler:
			c.buildHanders(a)
		default:
			log.Fatal(ErrHandleNotSupport)
			continue
		}
	}
	if len(handlers) > 0 {
		c.AddHandle(MethodUse, path, handlers)
	}
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

func (c *Core) ALL(path string, handler ...interface{}) error {
	for _, method := range Methods {
		if method == MethodUse {
			continue
		}
		if err := c.AddHandle(method, path, handler); err != nil {
			return err
		}
	}
	return nil
}

// Get add get method
//
//	 path string /foo
//	 handler core.Handle || http.HandlerFunc || http.Handler
//
//	 > add method
//
//	 c.Get("/foo", func(c *core.Ctx){
//			c.SendString("Hello world")
//	 })
func (c *Core) Get(path string, handler ...interface{}) error {
	return c.AddHandle(MethodGet, path, handler)
}

// Post add get method
//
//	> see Get
func (c *Core) Post(path string, handler ...interface{}) error {
	return c.AddHandle(MethodPost, path, handler)
}

// Head add get method
//
//	> see Get
func (c *Core) Head(path string, handler ...interface{}) error {
	return c.AddHandle(MethodHead, path, handler)
}

// Put add get method
//
//	> see Get
func (c *Core) Put(path string, handler ...interface{}) error {
	return c.AddHandle(MethodPut, path, handler)
}

// Delete add get method
//
//	> see Get
func (c *Core) Delete(path string, handler ...interface{}) error {
	return c.AddHandle(MethodDelete, path, handler)
}

// Connect add get method
//
//	> see Get
func (c *Core) Connect(path string, handler ...interface{}) error {
	return c.AddHandle(MethodConnect, path, handler)
}

// Options add get method
//
//	> see Get
func (c *Core) Options(path string, handler ...interface{}) error {
	return c.AddHandle(MethodOptions, path, handler)
}

// Trace add get method
//
//	> see Get
func (c *Core) Trace(path string, handler ...interface{}) error {
	return c.AddHandle(MethodTrace, path, handler)
}

// Patch add get method
//
//	> see Get
func (c *Core) Patch(path string, handler ...interface{}) error {
	return c.AddHandle(MethodPatch, path, handler)
}

func (c *Core) Static(relativePath, dirname string) *Core {
	c.AddHandle(MethodGet, path.Join(relativePath, ":staticfilepath"), func(ctx *Ctx) {
		file := ctx.GetParam("staticfilepath", "index.html")
		cur, _ := os.Getwd()
		filepath := path.Join(cur, dirname, file)
		http.ServeFile(ctx.W, ctx.R, filepath)
	}, true)
	return c
}

func (c *Core) StaticFS(relativePath string, ef *embed.FS) *Core {
	dirs, _ := ef.ReadDir(relativePath)
	subDir, _ := fs.Sub(ef, relativePath)
	fileServer := http.FileServer(http.FS(subDir))
	for _, rel := range dirs {
		switch {
		case rel.IsDir():
			c.AddHandle(MethodGet, path.Join(rel.Name(), ":fsfilepath"), func(ctx *Ctx) {
				file := ctx.GetParam("fsfilepath")
				filepath := path.Join(".", rel.Name(), file)
				c.handleFS(ctx, filepath, fileServer)
			}, true)
		case rel.Name() == "index.html":
			c.AddHandle(MethodGet, "/", func(ctx *Ctx) {
				filepath := path.Join(".", relativePath, "index.html")
				c.handleFS(ctx, filepath, fileServer)
			}, true)
		case rel.Name() == "favicon.ico":
			c.AddHandle(MethodGet, rel.Name(), func(ctx *Ctx) {
				filepath := path.Join(".", relativePath, "favicon.ico")
				c.handleFS(ctx, filepath, fileServer)
			}, true)
		}
	}
	return c
}

func (c *Core) handleFS(ctx *Ctx, filepath string, fshand http.Handler) {
	fshand.ServeHTTP(ctx.W, ctx.R)
}

// AddHandle
//
//	 methods string || []string
//	 path string /foo
//	 handler core.Handle || http.HandlerFunc || http.Handler
//
//	 app.AddHandle("GET", "/foo", func(c*core.Ctx){
//			c.SendString("hello world")
//	 })
func (c *Core) AddHandle(methods interface{}, path string, handler interface{}, static ...bool) error {
	if handler == nil {
		return ErrHandlerNotFound
	}
	if path == "" {
		path = "/"
	}
	D("%v: %s", methods, path)
	switch v := methods.(type) { // check method is string or []string
	case string:
		return c.tree.Insert([]string{v}, path, handler, static...)
	case []string:
		return c.tree.Insert(v, path, handler, static...)
	}
	return ErrMethodNotAllowed
}

func (c *Core) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "HEAD" {
		w.WriteHeader(204)
		return
	}

	ctx := c.assignCtx(w, r)
	defer c.releaseCtx(ctx)
	st := time.Now()
	result, err := c.tree.Find(r.Method, r.URL.Path)
	if err == nil {
		ctx.params = result.params
		ctx.handlers = append(result.preloads, result.handler...)
		if result.static {
			ctx.handlers = HandlerFuncs{result.handler[len(result.handler)-1]}
		}
		ctx.Next()
		// ctx.wm.DoWriteHeader()
		return
	}

	for u, p := range c.assets { // 静态解析
		prefix := fmt.Sprintf("/%s", u)
		if strings.HasPrefix(r.URL.Path, prefix) {
			cur, _ := os.Getwd()
			file := strings.TrimLeft(r.URL.Path, prefix)
			filepath := path.Join(cur, p.(string), file)
			http.ServeFile(w, r, filepath)
			return
		}
	}
	ctx.Set("request_duration", time.Since(st).String())
	c.NotFoundFunc(ctx, err)
}

func (c *Core) NotFound(ctx *Ctx, err error) {
	st := ctx.GetString("request_duration", "0")
	requestLog(StatusNotFound, ctx.Method(), ctx.Path(), st)
	ctx.SendStatus(http.StatusNotFound, err.Error())
}

// New New Core
func New(conf ...Options) *Core {
	c := &Core{
		tree:            NewTree(),
		assets:          make(Options),
		ViewFuncMap:     template.FuncMap{},
		enablePrefork:   false,
		networkProto:    "tcp4",
		RemoteIPHeaders: []string{"X-Forwarded-For", "X-Real-IP"},
		pool: sync.Pool{
			New: func() interface{} {
				return &Ctx{
					wm: resp{},
				}
			},
		},
		Server: &http.Server{},
	}
	c.Handler = c
	c.NotFoundFunc = c.NotFound
	if len(conf) > 0 {
		c.Conf = conf[0]
		Conf = c.Conf
		c.Debug = c.Conf.GetBool("debug")
		c.addr = c.Conf.ToString("listen", ":http")
		if !strings.Contains(c.addr, ":") {
			c.addr = fmt.Sprintf(":%s", c.addr)
		}
		c.Server.Addr = c.addr
		// 配置信息
		servConf := make(Options)
		Conf.GetAs("server", &servConf)
		if servConf != nil {
			c.Server.ReadTimeout = time.Second * time.Duration(Conf.GetInt("read_timeout", 10))
			c.Server.ReadHeaderTimeout = time.Second * time.Duration(Conf.GetInt("read_header_timeout", 5))
			c.Server.WriteTimeout = time.Second * time.Duration(Conf.GetInt("write_timeout", 10))
			c.Server.IdleTimeout = time.Second * time.Duration(Conf.GetInt("idle_timeout", 30))
		}
		c.enablePrefork = c.Conf.GetBool("prefork", false)
		c.assets = c.Conf.GetMap("static")
		c.networkProto = c.Conf.GetString("network", "tcp4")

		c.MaxMultipartMemory = c.Conf.GetInt64("maxMultipartMemory", defaultMultipartMemory)
	}
	return c
}

// Default init and use Logger And Recovery
func Default(conf ...Options) *Core {
	c := New(conf...)
	out := os.Stdout
	forceColor := c.Debug
	if log := c.Conf.GetString("log", ""); log != "" {
		var err error
		out, err = os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
		forceColor = false
		if err != nil {
			panic(err)
		}
		os.Stdout = out
	} else if c.Debug {
		out = os.Stdout
	}
	if c.Conf.GetBool("log_color") {
		forceColor = true
	}
	c.Use(Logger(LoggerConfig{ForceColor: forceColor, Output: out}), Recovery())
	if conf := c.Conf.GetMap("database"); conf != nil {
		NewModel(conf, c.Debug, forceColor)
	}
	return c
}

func (c *Core) GoListenAndServe(addr ...string) error {
	return c.GoListenAndServeContext(context.Background(), addr...)
}

func (c *Core) GoListenAndServeContext(ctx context.Context, addr ...string) error {
	if ctx == nil {
		return ErrContextMustBeSet
	}
	if len(addr) > 0 {
		c.addr = addr[0]
	}
	if c.addr == "" {
		c.addr = ":8080"
	}
	c.addr = FixedPort(c.addr)
	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		return err
	}

	if tcpLn, ok := ln.(*net.TCPListener); ok {
		ln = tcpKeepAliveListener{TCPListener: tcpLn}
	}

	return c.GoServe(ctx, ln)
}

func FixedPort(port string) string {
	if !strings.Contains(port, ":") {
		port = fmt.Sprintf(":%s", port)
	}
	return port
}

func (c *Core) GoServe(ctx context.Context, ln net.Listener) error {
	c.waiterMux.Lock()
	defer c.waiterMux.Unlock()
	c.waiter, ctx = errgroup.WithContext(ctx)
	c.waiter.Go(func() error {
		return c.Serve(ln)
	})
	go func(ctx context.Context) {
		<-ctx.Done()
		c.Close()
	}(ctx)
	return nil
}

func (c *Core) Wait() error {
	c.waiterMux.Lock()
	unset := c.waiter == nil
	c.waiterMux.Unlock()
	if unset {
		return ErrNotStartedYet
	}
	c.waiterMux.Lock()
	wait := c.waiter.Wait
	c.waiterMux.Unlock()
	err := wait()
	if err == http.ErrServerClosed {
		err = nil
	}
	return err
}

func (c *Core) ListenAndServe(addr ...string) error {
	if len(addr) > 0 {
		c.addr = addr[0]

		if c.addr == "" {
			c.addr = ":8080"
		}
		if !strings.Contains(c.addr, ":") {
			c.addr = fmt.Sprintf(":%s", c.addr)
		}
		c.Server.Addr = c.addr
	}

	if c.enablePrefork {
		return c.prefork()
	}

	ln, err := net.Listen("tcp", c.Server.Addr)
	if err != nil {
		return err
	}

	if tcpLn, ok := ln.(*net.TCPListener); ok {
		ln = tcpKeepAliveListener{TCPListener: tcpLn}
	}

	return c.Serve(ln)
}

func (c *Core) prefork() error {
	var (
		ln  net.Listener
		err error
	)
	if IsChild() {
		// use 1 cpu core per child process
		runtime.GOMAXPROCS(1)
		if ln, err = reuseport.Listen(c.networkProto, c.addr); err != nil {
			time.Sleep(sleepDuration)
			return fmt.Errorf("prefork: %w", err)
		}
		// kill current child proc when master exited
		go watchMaster()

		return c.Serve(ln)
	}

	type child struct {
		pid int
		err error
	}
	// create variables
	max := runtime.GOMAXPROCS(0)
	childs := make(map[int]*exec.Cmd)
	channel := make(chan child, max)

	// kill child procs when master exists
	defer func() {
		for _, proc := range childs {
			if err := proc.Process.Kill(); err != nil {
				if !errors.Is(err, os.ErrProcessDone) {
					log.Printf("prefork: failed to kill child: %v\n", err)
				}
			}
		}
	}()

	var pids []string
	for i := 0; i < max; i++ {
		cmd := exec.Command(os.Args[0], os.Args[1:]...) // nolint:gosec
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		cmd.Env = append(os.Environ(),
			fmt.Sprintf("%s=%s", envPreforkChildKey, envPreforkChildVal))
		if err = cmd.Start(); err != nil {
			return fmt.Errorf("failed to start a child prefork process, error: %w", err)
		}

		// store child process
		pid := cmd.Process.Pid
		childs[pid] = cmd
		pids = append(pids, strconv.Itoa(pid))

		go func() {
			channel <- child{pid, cmd.Wait()}
		}()
	}

	Info("start childs %s", strings.Join(pids, ","))

	return (<-channel).err
}
func (c *Core) Serve(ln net.Listener) error {
	port := strings.TrimPrefix(ln.Addr().String(), "[::]")
	if !strings.Contains(ln.Addr().String(), "127.0.0.1") {
		D("Listen: http://127.0.0.1%s\n", port)

		localIP, err := LocalIP()
		if err == nil {
			D("Listen: http://%s%s\n", localIP.String(), port)
		}
	} else {
		D("Listen: http://%s\n", port)
	}
	return c.Server.Serve(ln)
}

func (c *Core) Shutdown() error {
	// TODO: 需要修正 GoServe 退出请求
	return c.Server.Shutdown(context.TODO())
}

// SetFuncMap sets the FuncMap used for template.FuncMap.
func (c *Core) SetFuncMap(funcMap template.FuncMap) *Core {
	c.ViewFuncMap = funcMap
	return c
}

func LocalIP() (ip net.IP, err error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return
	}
	defer conn.Close()

	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP, nil
}

type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (net.Conn, error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return nil, err
	}
	if err = tc.SetKeepAlive(true); err != nil {
		return nil, err
	}
	return tc, err
}
