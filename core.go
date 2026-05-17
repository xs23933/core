package core

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/xs23933/core/v3/etcd"
	"github.com/xs23933/core/v3/middleware/view"
	"github.com/xs23933/core/v3/reuseport"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/http2"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

type CertMagicConfig struct {
	APIToken string
	Email    string
	Domains  []string
	CacheDir string
}

type Core struct {
	*http.Server
	h3    *http3.Server
	mutex sync.Mutex

	trees []*RouteNode

	Conf           Options
	assets         Options
	Debug          bool
	addr           string
	RequestMethods []string

	pool               sync.Pool
	ErrorHandler       ErrorHandler
	eg                 *errgroup.Group
	Ctx                context.Context
	stop               context.CancelFunc
	defaultRestful     RestfulDefine
	modName            string
	MaxMultipartMemory int64
	enablePrefork      bool
	networkProto       string
	Views              view.IEngine
	TextEngine         view.ITextEngine
	// acme
	certManager *autocert.Manager

	// certmagic
	certMagicConfig  CertMagicConfig
	certMagicEnabled bool

	// 添加 gRPC 支持
	grpcServer  *grpc.Server
	grpcAddr    string
	grpcEnabled bool

	// etcd 相关
	etcdRegistry  *etcd.Registry
	EtcdDiscovery *etcd.Discovery
	shutdownHooks []func()
}

// core implements Router.
func (app *Core) core() *Core {
	return app
}

func New(options ...Options) *Core {
	app := &Core{
		Server:         &http.Server{},
		addr:           ":8080",
		Debug:          true,
		defaultRestful: defaultRestful,
		modName:        "mod",
		enablePrefork:  false,
		networkProto:   "tcp4",
		Conf: Options{
			"debug": true,
		},
		MaxMultipartMemory: defaultMultipartMemory,
	}

	out := os.Stdout
	colorful := app.Debug
	if len(options) > 0 {
		app.Conf = options[0]
		app.Debug = app.Conf.GetBool("debug", false)
		app.addr = app.Conf.ToString("listen", ":8000")
		restful := app.Conf.GetMap("restful")
		status := restful.GetString("status")
		if status != "" {
			app.defaultRestful.Status = status
			app.defaultRestful.Data = restful.GetString("data")
			app.defaultRestful.Message = restful.GetString("message")
			app.defaultRestful.Code = restful.GetInt("code", 0)
		}
		if !strings.Contains(app.addr, ":") {
			app.addr = fmt.Sprintf(":%s", app.addr)
		}
		app.assets = app.Conf.GetMap("static")

		app.modName = app.Conf.GetString("mod_prefix", "mod")

		if log := app.Conf.GetString("log", ""); log != "" {
			var err error
			out, err = os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
			colorful = false
			if err != nil {
				panic(err)
			}
			os.Stdout = out
		} else if app.Debug {
			out = os.Stdout
		}
		if app.Conf.GetBool("colorful") {
			colorful = true
		}
		if app.Conf.GetBool("prefork", false) {
			app.enablePrefork = true
		}

		app.networkProto = app.Conf.GetString("network", "tcp4")

		acme := Options{}
		if err := app.Conf.GetAs("acme", &acme); err == nil {
			domains := acme.GetStrings("domains", []string{})
			email := acme.GetString("email", "")
			if len(domains) > 0 {
				app.certManager = &autocert.Manager{
					Prompt:     autocert.AcceptTOS,
					Email:      email,
					HostPolicy: autocert.HostWhitelist(domains...),
					Cache:      autocert.DirCache(acme.GetString("cache", "./certs")),
				}
				app.addr = ":https"
			}
		}

		// certMagic
		certMagic := Options{}
		if err := app.Conf.GetAs("certmagic", &certMagic); err == nil {
			token := certMagic.GetString("api_token", "")
			domains := certMagic.GetStrings("domains", []string{})
			email := certMagic.GetString("email", "")
			if token != "" && len(domains) > 0 {
				app.certMagicConfig = CertMagicConfig{
					APIToken: token,
					Email:    email,
					Domains:  domains,
					CacheDir: certMagic.GetString("cache", "./certs"),
				}
				app.certMagicEnabled = true
				// app.addr = ":https"
			} else if email != "" { // 使用 onDemand
				app.certMagicConfig = CertMagicConfig{
					Email:    email,
					Domains:  domains,
					CacheDir: certMagic.GetString("cache", "./certs"),
				}
				app.certMagicEnabled = false
				// app.addr = ":https"
			}
		}

		app.MaxMultipartMemory = app.Conf.GetInt64("max_multipart_memory", defaultMultipartMemory)
	}
	Conf = app.Conf

	app.RequestMethods = Conf.GetStrings("methods", Methods[:len(Methods)-1])

	// 为每个 HTTP 方法初始化一个 Trie 根节点
	app.trees = make([]*RouteNode, len(app.RequestMethods))
	for i := range app.trees {
		app.trees[i] = &RouteNode{
			path:        "/",
			nType:       root,
			staticChild: make(map[string]*RouteNode),
			paramChild:  nil,
			catchChild:  nil,
			handlers:    nil,
		}
	}

	app.ErrorHandler = DefaultErrorHandler

	ctx, cancel := context.WithCancel(context.Background())
	app.eg, app.Ctx = errgroup.WithContext(ctx)
	app.stop = cancel

	app.pool = sync.Pool{
		New: func() any {
			return &BaseCtx{
				wm: resp{},
			}
		},
	}

	c := make(chan os.Signal, 1)
	const SIGUSR2 = syscall.Signal(0x1f)
	// SIGINT  中断信号通常 Ctrl+c
	// SIGTERM 终止信号, 系统使用它来请求进程正常终止
	// SIGHUP  终端控制关闭时发生
	// SIGQUIT 终止信号, 无法被捕获
	// signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM,
		syscall.SIGQUIT, SIGUSR2)
	go func() {
		s := <-c
		Info("Received signal: %v, shutting down...", s)
		app.shutdown()
		cancel()
	}()

	app.Use(Logger(LoggerConfig{ForceColor: colorful, App: app, Debug: app.Debug, Output: out}), Recovery())

	if conf := Conf.GetMap("database"); len(conf) > 0 {
		if _, err := NewModel(conf, app.Debug, colorful); err != nil {
			Erro("disconnect %s: %w", conf.GetString("dsn"), err)
		}
	}
	if conf := Conf.GetMap("redis"); len(conf) > 0 {
		if _, err := NewRedis(conf, app.Debug); err != nil {
			Erro("redis connect failed: %v", err)
		}
		app.OnShutdown(func() { CloseRedis() })
	}
	if conf := Conf.GetMap("nsq"); len(conf) > 0 {
		if err := InitNSQ(conf, app.Debug); err != nil {
			Erro("nsq init failed: %v", err)
		}
		app.OnShutdown(func() { CloseNSQ() })
	}
	if !IsChild() {
		Log(CoreHeader, VERSION)
	}

	for k, v := range app.assets {
		prefix := fmt.Sprintf("/%s", k)
		app.Static(prefix, v.(string))
	}

	// 初始化 gRPC 配置
	app.grpcEnabled = app.Conf.GetBool("grpc.enabled", false)
	app.grpcAddr = app.Conf.GetString("grpc.addr")

	return app
}

func (app *Core) Listen(port ...any) error {
	if len(port) > 0 {
		switch v := port[0].(type) {
		case int, uint, int16, int32:
			app.addr = fmt.Sprintf(":%d", v)
		case string:
			app.addr = v
		}
	}
	app.Handler = app

	if app.enablePrefork {
		return app.prefork()
	}

	ln, err := reuseport.Listen(app.networkProto, app.addr)
	if err != nil {
		return err
	}

	if tcpLn, ok := ln.(*net.TCPListener); ok {
		ln = tcpKeepAliveListener{TCPListener: tcpLn}
	}
	return app.Serve(ln)
}

// Run 启动应用程序并监听指定端口
// 参数 port 是可选的端口号，可以是一个或多个值
// 返回可能发生的错误
func (app *Core) Run(port ...any) error {
	return app.Listen(port...)
}

func (app *Core) Serve(ln net.Listener) error {

	port := strings.TrimPrefix(ln.Addr().String(), "[::]")
	port = strings.TrimPrefix(port, "0.0.0.0:")
	if !strings.Contains(ln.Addr().String(), "127.0.0.1") {
		D("Listen: http://127.0.0.1:%s\n", port)

		localIP, err := LocalIP()
		if err == nil {
			D("Listen: http://%s:%s\n", localIP.String(), port)
		}
	} else {
		D("Listen: http://%s\n", port)
	}
	app.runProcess()

	// 启动 gRPC 服务器（如果启用）
	if err := app.startGRPCServer(); err != nil {
		return err
	}

	// 确保关闭时也关闭 gRPC
	defer app.shutdownGRPC()

	// 优先使用 certMagic
	if app.certMagicEnabled || app.certMagicConfig.Email != "" {
		if app.certMagicEnabled {
			if err := app.setupCertMagic(); err != nil {
				Erro("CertMagic setup error: %v", err)
				return err
			}
		} else if app.certMagicConfig.Email != "" {
			if err := app.onDemand(app.certMagicConfig.Email, app.certMagicConfig.CacheDir); err != nil {
				Erro("On-demand certificate error: %v", err)
				return err
			}
		}

		app.eg.Go(func() error {
			ln, err := reuseport.ListenUDPWithReusePort("udp", ":https")
			if err != nil {
				Erro("UDP listener(%s) error: %v", app.addr, err)
				return err
			}
			defer ln.Close()
			tls3 := &tls.Config{
				MinVersion:     app.TLSConfig.MinVersion,
				GetCertificate: app.TLSConfig.GetCertificate,
				NextProtos:     append([]string{"h3"}, app.TLSConfig.NextProtos...),
			}
			app.h3 = &http3.Server{
				Addr:      app.addr,
				Handler:   app,
				TLSConfig: tls3,
			}
			Info("Starting HTTP/3 (%s) server", ":https")
			return app.h3.Serve(ln)
		})
		tls := &tls.Config{
			MinVersion:     app.TLSConfig.MinVersion,
			GetCertificate: app.TLSConfig.GetCertificate,
			NextProtos:     append([]string{"h2"}, app.TLSConfig.NextProtos...),
		}
		app.Server.TLSConfig = tls
		http2.ConfigureServer(app.Server, &http2.Server{})

		// CertMagic 的 TLS 配置已经设置好了
		if err := app.Server.ServeTLS(ln, "", ""); err != nil {
			return err
		}
		return nil

	}

	if app.certManager != nil {
		app.Server.TLSConfig = &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: app.certManager.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
		}
		http2.ConfigureServer(app.Server, &http2.Server{})

		app.GET("/.well-known/acme-challenge/*", app.certManager.HTTPHandler(nil))
		app.GET("/.health", func(c Ctx) {
			c.SendStatus(200, "ok")
		})
		Info("ACME Enabled, serving HTTPS on :443")
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/.well-known/acme-challenge/", app.certManager.HTTPHandler(nil))
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				target := "https://" + r.Host + r.URL.String()
				http.Redirect(w, r, target, http.StatusMovedPermanently)
			})
			httpSrv := &http.Server{Handler: mux}
			if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				Erro("HTTP server error: %v", err)
			}
		}()

		if err := app.Server.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			Erro(" ACME Error: %v", err)
			return err
		}
		return nil
	}
	Info("Serving HTTP on %s", ln.Addr().String())
	if err := app.Server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

func (app *Core) prefork() error {
	var (
		ln  net.Listener
		err error
	)
	if IsChild() {
		// use 1 cpu core per child process
		runtime.GOMAXPROCS(1)
		if ln, err = reuseport.Listen(app.networkProto, app.addr); err != nil {
			time.Sleep(sleepDuration)
			return fmt.Errorf("prefork: %w", err)
		}
		// kill current child proc when master exited
		go watchMaster()

		app.runProcess()
		return app.Serve(ln)
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
	for range max {
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

func (app *Core) runProcess() {
	app.loadMods() // load modules
	// app.buildTree()
}

func (app *Core) Use(fn ...any) Router {
	prefixes, handlers := anyToHandlers(app, fn...)
	if len(handlers) > 0 {
		for _, prefix := range prefixes {
			if prefix == "/" || prefix == "" { // 全局中间件
				for _, root := range app.trees {
					root.middlewares = append(handlers, root.middlewares...)
				}
			} else {
				for _, root := range app.trees {
					node := root.addRouteNode(prefix)
					node.middlewares = append(node.middlewares, handlers...)
				}
			}
		}
	}
	return app
}

// Get registers a route for GET methods that requests a representation
// of the specified resource. Requests using GET should only retrieve data.
//

func (app *Core) GET(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodGet}, path, handler, middleware...)
}
func (app *Core) HEAD(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodHead}, path, handler, middleware...)
}
func (app *Core) POST(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodPost}, path, handler, middleware...)
}
func (app *Core) PUT(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodPut}, path, handler, middleware...)
}
func (app *Core) DELETE(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodDelete}, path, handler, middleware...)
}
func (app *Core) CONNECT(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodConnect}, path, handler, middleware...)
}
func (app *Core) OPTIONS(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodOptions}, path, handler, middleware...)
}
func (app *Core) TRACE(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodTrace}, path, handler, middleware...)
}
func (app *Core) PATCH(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodPatch}, path, handler, middleware...)
}

func (app *Core) ALL(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodAll}, path, handler, middleware...)
}

func (app *Core) Static(relativePath, root string) Router {
	return app.StaticFS(relativePath, Dir(root, false))
}

func (app *Core) StaticFS(relativePath string, fs http.FileSystem) Router {
	if strings.Contains(relativePath, ":") || strings.Contains(relativePath, "*") {
		panic("URL parameters can not be used when serving a static folder")
	}
	handle := app.staticHandler(relativePath, fs)
	uri := path.Join(relativePath, "*")
	app.GET(uri, handle)
	app.HEAD(uri, handle)
	return app
}

// StaticFile static file
//
// app.StaticFile("/favicon.ico", "./favicon.ico")
func (app *Core) StaticFile(relativePath, dirname string) Router {
	return app.staticFileHandler(relativePath, func(c Ctx) error {
		c.File(dirname)
		return nil
	})
}

// StaticFileFS works just like `StaticFile` but a custom `http.FileSystem` can be used instead..
//
// app.StaticFileFS("favicon.ico", "./resources/favicon.ico", Dir{".", false})
func (app *Core) StaticFileFS(relativePath, dirname string, fs http.FileSystem) Router {
	return app.staticFileHandler(relativePath, func(c Ctx) error {
		c.FileFromFS(dirname, fs)
		return nil
	})
}

func (app *Core) staticFileHandler(relativePath string, handler HandlerFunc) Router {
	if strings.Contains(relativePath, ":") || strings.Contains(relativePath, "*") {
		panic("URL parameters can not be used when serving a static file")
	}
	app.GET(relativePath, handler)
	app.HEAD(relativePath, handler)
	return app
}

func (app *Core) staticHandler(relativePath string, fs http.FileSystem) HandlerFunc {
	absolutePath := joinPaths("", relativePath)
	fileServer := http.StripPrefix(absolutePath, http.FileServer(fs))

	return HandlerFunc(func(c Ctx) error {
		if _, noListing := fs.(*onlyFilesFS); noListing {
			c.SendStatus(StatusNotFound)
		}
		fileServer.ServeHTTP(c.Response(), c.Request())
		return nil
	})
}
func (app *Core) Add(methods []string, path string, handler any, middleware ...any) Router {
	D("route: %s %s", strings.Join(methods, ","), path)

	return app.AddHandle(methods, path, nil, handler, app.ProcessedHandler(middleware)...)
}

func (app *Core) Group(prefix string, handlers ...HandlerFuncs) Router {
	g := &Group{
		Prefix: prefix,
		Core:   app,
	}
	if len(handlers) > 0 {
		app.AddHandle([]string{MethodUse}, prefix, g, nil, app.ProcessedHandler(handlers)...)
	}
	return g
}

func anyToHandlers(app *Core, fn ...any) (prefixes []string, handlers HandlerFuncs) {
	var (
		prefix string
	)
	handlers = make(HandlerFuncs, 0)

	for _, v := range fn {
		switch arg := v.(type) {
		case string:
			prefix = arg
		case []string:
			prefixes = arg
		case view.ITextEngine:
			app.TextEngine = arg
		case view.IEngine:
			app.Views = arg
		case HandlerFun, HandlerFunc, http.HandlerFunc, http.Handler:
			handlers = append(handlers, app.ProcessedHandler(arg)...)
		case HandlerFuncs:
			handlers = append(handlers, arg...)
		default:
			panic(fmt.Sprintf("use: invalid middleware %v\n", reflect.TypeOf(arg)))
		}
	}

	if len(prefixes) == 0 {
		prefixes = append(prefixes, prefix)
	}
	return prefixes, handlers
}
