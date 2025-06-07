package core

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
)

// Router defines all router handle interface, including app and group router.
type Router interface {
	Use(args ...any) Router
	core() *Core
	GET(path string, handler any, middleware ...any) Router
	HEAD(path string, handler any, middleware ...any) Router
	POST(path string, handler any, middleware ...any) Router
	PUT(path string, handler any, middleware ...any) Router
	DELETE(path string, handler any, middleware ...any) Router
	CONNECT(path string, handler any, middleware ...any) Router
	OPTIONS(path string, handler any, middleware ...any) Router
	TRACE(path string, handler any, middleware ...any) Router
	PATCH(path string, handler any, middleware ...any) Router
	ALL(path string, handler any, middleware ...any) Router
}

type Route struct {
	// Data for routing
	pos         uint32      // Position in stack -> important for the sort of the matched routes
	use         bool        // USE matches path prefixes
	star        bool        // Path equals '*'
	root        bool        // Path equals '/'
	path        string      // Prettified path
	routeParser routeParser // Parameter parser
	group       *Group      // Group instance. used for routes in groups

	// Public fields
	Method string `json:"method"` // HTTP method
	Name   string `json:"name"`   // Route's name
	//nolint:revive // Having both a Path (uppercase) and a path (lowercase) is fine
	Path     string        `json:"path"`   // Original registered route path
	Params   []string      `json:"params"` // Case sensitive param keys
	Handlers []HandlerFunc `json:"-"`      // Ctx handlers
}

func (r *Route) match(detectionPath, path string, params *[maxParams]string) bool {
	// root detectionPath check
	if r.root && detectionPath == "/" {
		return true
		// '*' wildcard matches any detectionPath
	} else if r.star {
		if len(path) > 1 {
			params[0] = path[1:]
		} else {
			params[0] = ""
		}
		return true
	}
	// Does this route have parameters
	if len(r.Params) > 0 {
		// Match params
		if match := r.routeParser.getMatch(detectionPath, path, params, r.use); match {
			// Get params from the path detectionPath
			return match
		}
	}
	// Is this route a Middleware?
	if r.use {
		// Single slash will match or detectionPath prefix
		if r.root || strings.HasPrefix(detectionPath, r.path) {
			return true
		}
		// Check for a simple detectionPath match
	} else if len(r.path) == len(detectionPath) && r.path == detectionPath {
		return true
	}
	// No match
	return false
}

func (app *Core) processedHandler(hand any) HandlerFuncs {
	hands := make(HandlerFuncs, 0)

	switch h := hand.(type) {
	case HandlerFunc:
		hands = append(hands, h)
	case HandlerFuncs:
		hands = append(hands, h...)
	case HandlerFun:
		hands = append(hands, func(c Ctx) error {
			h(c)
			return nil
		})
	case []any:
		has := make(HandlerFuncs, 0)
		for _, v := range h {
			has = append(has, app.processedHandler(v)...)
		}
		hands = append(hands, has...)
	case http.HandlerFunc:
		hands = append(hands, HandlerFunc(func(c Ctx) error {
			h(c.Response(), c.Request())
			return nil
		}))
	case func(http.ResponseWriter, *http.Request):
		hands = append(hands, HandlerFunc(func(c Ctx) error {
			h(c.Response(), c.Request())
			return nil
		}))
	case http.Handler:
		hands = append(hands, HandlerFunc(func(c Ctx) error {
			h.ServeHTTP(c.Response(), c.Request())
			return nil
		}))
	default:
		panic(fmt.Sprintf("use: invalid handler %v\n", reflect.TypeOf(h)))
	}
	return hands
}

// 检查 HTTP 方法是否合法
func (app *Core) validateMethod(method string) error {
	if method != MethodUse && methodPos(method) == -1 {
		return fmt.Errorf("add: invalid http method %s", method)
	}
	return nil
}

// 确保路径以 "/" 开头
func (app *Core) preparePath(uri string) string {
	if uri == "" {
		return "/"
	}
	if uri[0] != '/' {
		return "/" + uri
	}
	return uri
}

// 根据配置调整路径大小写
func (app *Core) adjustPathCase(uri string) string {
	uriPretty := uri
	if !Conf.GetBool("case-sensitive", false) {
		uriPretty = strings.ToLower(uriPretty)
	}
	if !Conf.GetBool("strict-routing", false) && len(uriPretty) > 1 {
		uriPretty = strings.TrimRight(uriPretty, "/")
	}
	return uriPretty
}

func (app *Core) AddHandle(methods []string, uri string, group *Group, handler any, middleware ...HandlerFunc) Router {
	handlers := middleware
	if handler != nil {
		handlers = append(handlers, app.processedHandler(handler)...)
	}
	// 合并中间件和处理器
	// handlers := append(middleware, app.processedHandler(handler)...)
	if len(handlers) == 0 {
		panic(fmt.Sprintf("missing handler/middleware in route: %s\n", uri))
	}

	// 确保路径格式
	uri = app.preparePath(uri)
	uriPretty := app.adjustPathCase(uri)

	// 是否为星号和根路径
	isStar := uri == "/*"
	isRoot := uri == "/"
	parsedUri := parseRoute(uri)
	parsedPretty := parseRoute(uriPretty)

	for _, method := range methods {
		method := strings.ToUpper(method)
		if err := app.validateMethod(method); err != nil {
			panic(err)
		}

		route := Route{
			use:  method == MethodUse,
			star: isStar,
			root: isRoot,

			// Path data
			path:        RemoveEscapeChar(uriPretty),
			routeParser: parsedPretty,
			Params:      parsedUri.params,
			// Group data
			group: group,

			// Public data
			Path:     uri,
			Method:   method,
			Handlers: handlers,
		}

		// Increment global handler count
		atomic.AddUint32(&app.handlersCount, uint32(len(handlers)))

		// Middleware route matches all HTTP methods
		if route.use {
			// Add route to all HTTP methods stack
			for _, m := range app.RequestMethods {
				r := route
				app.addRoute(m, &r)
			}
		} else {
			// Add route to stack
			app.addRoute(method, &route)
		}
	}
	return app
}

func (app *Core) addRoute(method string, route *Route, isMounted ...bool) {
	// Check mounted routes
	var mounted bool
	if len(isMounted) > 0 {
		mounted = isMounted[0]
	}

	// Get unique HTTP method identifier
	m := methodPos(method)
	// prevent identically route registeration
	l := len(app.stack[m]) - 1
	if l > 0 {
		lastRoute := app.stack[m][l]
		if lastRoute.Path == route.Path && lastRoute.use == route.use {
			lastRoute.Handlers = append(lastRoute.Handlers, route.Handlers...)
			// Execute onRoute hooks & change latestRoute if not adding mounted route
			if !mounted {
				app.mutex.Lock()
				app.latestRoute = route
				app.mutex.Unlock()
			}
			return
		}
	}
	// Increment global route position
	route.pos = atomic.AddUint32(&app.routesCount, 1)
	route.Method = method
	// Add route to the stack
	app.stack[m] = append(app.stack[m], route)
	app.routesRefreshed = true

	// Execute onRoute hooks & change latestRoute if not adding mounted route
	if !mounted {
		app.mutex.Lock()
		app.latestRoute = route
		app.mutex.Unlock()
	}

	// 排序
	sort.SliceStable(app.stack[m], func(i, j int) bool {
		return app.stack[m][i].pos < app.stack[m][j].pos
	})
}

func (app *Core) buildTree() *Core {
	if !app.routesRefreshed {
		return app
	}

	// loop all the methods and stacks and create the previously registered routes
	for m := range app.RequestMethods {
		tsMap := make(map[string][]*Route)
		for _, route := range app.stack[m] {
			treePath := ""
			if len(route.routeParser.segs) > 0 && len(route.routeParser.segs[0].Const) >= 3 {
				treePath = route.routeParser.segs[0].Const[:3]
			}
			// create tree stack
			tsMap[treePath] = append(tsMap[treePath], route)
		}
		app.treeStack[m] = tsMap
	}

	// loop the methods and tree stacks and add global stack and sort everything
	for m := range app.RequestMethods {
		tsMap := app.treeStack[m]
		for treePart := range tsMap {
			if treePart != "" {
				// merge glbal tree routes in current tree stack
				tsMap[treePart] = uniqueRouteStack(append(tsMap[treePart], tsMap[""]...))
			}
			// sort tree slices with the positions
			slc := tsMap[treePart]
			sort.Slice(slc, func(i, j int) bool { return slc[i].pos < slc[j].pos })
		}
	}
	app.routesRefreshed = false
	return app
}

func (app *Core) next(c *BaseCtx) (bool, error) {
	// Get stack length
	tree, ok := app.treeStack[c.methodInt][c.treePath]
	if !ok {
		tree = app.treeStack[c.methodInt][""]
	}
	lenr := len(tree) - 1

	// Loop over the route stack starting from previous index
	for c.indexRoute < lenr {
		c.indexRoute++

		// Get *Route
		route := tree[c.indexRoute]

		// Check if it matches the request path
		match := route.match(c.detectionPath, c.path, &c.values)
		if !match {
			// No match, next route
			continue
		}
		// Pass route reference and param values
		c.route = route

		// Non use handler matched
		if !c.matched && !route.use {
			c.matched = true
		}

		// Execute first handler of route
		c.indexHandler = 0
		err := route.Handlers[0](c)
		return match, err // Stop scanning the stack
	}

	// If c.Next() does not match, return 404
	err := NewError(StatusNotFound, c.method+" "+c.path+" "+"Not found")
	if !c.matched && app.methodExist(c) {
		// If no match, scan stack again if other methods match the request
		// Moved from app.handler because middleware may break the route chain
		err = ErrNotFound
	}
	return false, err
}

func (app *Core) methodExist(c *BaseCtx) bool {
	var exists bool

	methods := app.RequestMethods
	for i := range methods {
		// Skip original method
		if int(c.methodInt) == i {
			continue
		}
		// Reset stack index
		c.indexRoute = -1

		tree, ok := c.app.treeStack[i][c.treePath]
		if !ok {
			tree = c.app.treeStack[i][""]
		}
		// Get stack length
		lenr := len(tree) - 1
		// Loop over the route stack starting from previous index
		for c.indexRoute < lenr {
			// Increment route index
			c.indexRoute = c.indexRoute + 1
			// Get *Route
			route := tree[c.indexRoute]
			// Skip use routes
			if route.use {
				continue
			}
			// Check if it matches the request path
			match := route.match(c.detectionPath, c.Path(), c.getValues())
			// No match, next route
			if match {
				// We matched
				exists = true
				// Add method to Allow handler
				c.Append(HeaderAllow, methods[i])
				// Break stack loop
				break
			}
		}
	}
	return exists
}

func (app *Core) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, ok := app.AcquireCtx(w, r).(*BaseCtx)
	if !ok {
		panic("field to type-assert to Ctx")
	}
	defer app.ReleaseCtx(c)

	// handle invalid http method directly
	if methodPos(c.method) == -1 {
		_ = c.SendStatus(StatusNotImplemented)
		return
	}

	if _, err := app.next(c); err != nil {
		if r := c.app.ErrorHandler(c, err); r != nil {
			_ = c.SendStatus(StatusInternalServerError)
		}
	}
}
