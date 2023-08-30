package core

import (
	"context"
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

	"github.com/xs23933/core/v2/middleware/view"
	"github.com/xs23933/core/v2/reuseport"
	"golang.org/x/sync/errgroup"
)

type Core struct {
	*http.Server
	mutex sync.Mutex
	// Route stack divided by HTTP methods
	stack [][]*Route
	// Route stack divided by HTTP methods and route prefixes
	treeStack []map[string][]*Route
	// Amount of registered routes
	routesCount uint32
	// Amount of registered handlers
	handlersCount uint32
	// contains the information if the route stack has been changed to build the optimized tree
	routesRefreshed bool
	// Latest route & group
	latestRoute    *Route
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
}

// core implements Router.
func (app *Core) core() *Core {
	return app
}

func New(options ...Options) *Core {
	app := &Core{
		latestRoute:    &Route{},
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
		if app.Conf.GetBool("prefork") {
			app.enablePrefork = true
		}

		app.networkProto = app.Conf.GetString("network", "tcp4")

		app.MaxMultipartMemory = app.Conf.GetInt64("max_multipart_memory", defaultMultipartMemory)
	}
	Conf = app.Conf

	app.RequestMethods = Conf.GetStrings("methods", Methods[:len(Methods)-2])

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

	app.stack = make([][]*Route, len(app.RequestMethods))
	app.treeStack = make([]map[string][]*Route, len(app.RequestMethods))

	c := make(chan os.Signal, 1)
	const SIGUSR2 = syscall.Signal(0x1f)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM,
		syscall.SIGQUIT, SIGUSR2)
	go func() {
		for range c {
			cancel()
		}
	}()

	app.Use(Logger(LoggerConfig{ForceColor: colorful, App: app, Debug: app.Debug, Output: out}), Recovery())

	if conf := Conf.GetMap("database"); conf != nil {
		NewModel(conf, app.Debug, colorful)
	}
	if !IsChild() {
		Log(CoreHeader, VERSION)
	}
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

func (app *Core) Serve(ln net.Listener) error {

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
	app.runProcess()
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

func (app *Core) runProcess() {
	app.loadMods() // load modules
	app.buildTree()
}
func (app *Core) Use(fn ...any) Router {
	prefixes, handlers := anyToHandlers(app, fn...)
	if len(handlers) > 0 {
		for _, prefix := range prefixes {
			app.AddHandle([]string{MethodUse}, prefix, nil, nil, app.processedHandler(handlers)...)
		}
	}
	return app
}

// Get registers a route for GET methods that requests a representation
// of the specified resource. Requests using GET should only retrieve data.
//

func (app *Core) Get(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodGet}, path, handler, middleware...)
}
func (app *Core) Head(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodHead}, path, handler, middleware...)
}
func (app *Core) Post(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodPost}, path, handler, middleware...)
}
func (app *Core) Put(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodPut}, path, handler, middleware...)
}
func (app *Core) Delete(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodDelete}, path, handler, middleware...)
}
func (app *Core) Connect(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodConnect}, path, handler, middleware...)
}
func (app *Core) Options(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodOptions}, path, handler, middleware...)
}
func (app *Core) Trace(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodTrace}, path, handler, middleware...)
}
func (app *Core) Patch(path string, handler any, middleware ...any) Router {
	return app.Add([]string{MethodPatch}, path, handler, middleware...)
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
	app.Get(uri, handle)
	app.Head(uri, handle)
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
	app.Get(relativePath, handler)
	app.Head(relativePath, handler)
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
	handlers := middleware
	if handler != nil {
		handlers = append(handlers, handler)
	}
	D("route: %s %s", strings.Join(methods, ","), path)

	return app.AddHandle(methods, path, nil, handler, app.processedHandler(handlers)...)
}

func (app *Core) Group(prefix string, handlers ...any) Router {
	g := &Group{
		Prefix: prefix,
		Core:   app,
	}
	_, handlers = anyToHandlers(app, handlers...)
	if len(handlers) > 0 {
		app.AddHandle([]string{MethodUse}, prefix, g, nil, app.processedHandler(handlers)...)
	}
	return g
}

func anyToHandlers(app *Core, fn ...any) (prefixes []string, handlers []any) {
	var (
		prefix string
	)
	handlers = make([]any, 0)

	for _, v := range fn {
		switch arg := v.(type) {
		case string:
			prefix = arg
		case []string:
			prefixes = arg
		case view.IEngine:
			app.Views = arg
		case HandlerFun, HandlerFunc, http.HandlerFunc, http.Handler:
			handlers = append(handlers, arg)
		case HandlerFuncs:
			handlers = append(handlers, arg)
		default:
			panic(fmt.Sprintf("use: invalid middleware %v\n", reflect.TypeOf(arg)))
		}
	}

	if len(prefixes) == 0 {
		prefixes = append(prefixes, prefix)
	}
	return prefixes, handlers
}
