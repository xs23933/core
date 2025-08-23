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
	if r.root && len(path) == 1 && detectionPath[0] == '/' {
		return true
		// '*' wildcard matches any detectionPath
	} else if r.star {
		if len(path) > 1 {
			params[0] = path
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
	isStar := uri == "/*" || uri == "*" || strings.HasPrefix(uri, "/*")
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

func (app *Core) Build() *Core {
	return app.buildTree()
}

func (app *Core) buildTree() *Core {
	if !app.routesRefreshed {
		return app
	}
	app.routesRefreshed = false

	for _, method := range app.RequestMethods {
		m := methodPos(method)
		if m == -1 {
			continue
		}

		tsMap := make(map[string][]*Route)

		// 按前缀分桶 - 修复分类逻辑
		for _, route := range app.stack[m] {
			treePath := ""

			// 通配路由放在 "" 桶
			if route.star {
				treePath = ""
			} else if route.root {
				treePath = ""
			} else if len(route.routeParser.segs) > 0 && len(route.routeParser.segs[0].Const) >= 3 {
				// 普通路由按前3个字符分桶
				treePath = route.routeParser.segs[0].Const[:3]
			} else {
				// 其他情况也放到 "" 桶
				treePath = ""
			}
			tsMap[treePath] = append(tsMap[treePath], route)
		}

		// 确保通配路由在所有桶中都存在
		if starRoutes, ok := tsMap[""]; ok && len(starRoutes) > 0 {
			for k := range tsMap {
				if k != "" {
					// 合并并去重
					merged := make([]*Route, len(tsMap[k]))
					copy(merged, tsMap[k])

					for _, starRoute := range starRoutes {
						// 只添加通配路由
						if starRoute.star && !containsRoute(merged, starRoute) {
							merged = append(merged, starRoute)
						}
					}
					tsMap[k] = merged
				}
			}
		}

		// 排序每个桶 - 确保通配路由在最后
		for k := range tsMap {
			sort.Slice(tsMap[k], func(i, j int) bool {
				// 通配路由放在最后
				if tsMap[k][i].star && !tsMap[k][j].star {
					return false
				}
				if !tsMap[k][i].star && tsMap[k][j].star {
					return true
				}
				// 中间件路由放在前面
				if tsMap[k][i].use && !tsMap[k][j].use {
					return true
				}
				if !tsMap[k][i].use && tsMap[k][j].use {
					return false
				}
				// 普通路由按位置排序
				return tsMap[k][i].pos < tsMap[k][j].pos
			})
		}

		app.treeStack[m] = tsMap
	}

	return app
}

// 辅助函数：检查路由是否已存在
func containsRoute(routes []*Route, route *Route) bool {
	for _, r := range routes {
		if r == route {
			return true
		}
	}
	return false
}

func (app *Core) next(c *BaseCtx) (bool, error) {
	if app.routesRefreshed {
		app.buildTree()
	}
	// 获取当前方法的路由树
	tree, ok := app.treeStack[c.methodInt][c.treePath]
	if !ok {
		tree = app.treeStack[c.methodInt][""]
	}
	lenr := len(tree) - 1

	// 遍历路由栈
	for c.indexRoute < lenr {
		c.indexRoute++
		route := tree[c.indexRoute]

		// 检查是否匹配请求路径
		match := route.match(c.detectionPath, c.path, &c.values)

		if !match {
			continue
		}

		// 匹配成功
		c.route = route
		if !c.matched && !route.use {
			c.matched = true
		}

		// 执行第一个处理器
		c.indexHandler = 0
		err := route.Handlers[0](c)
		return true, err
	}

	// 检查是否有通配路由
	for _, route := range tree {
		if route.star {
			match := route.match(c.detectionPath, c.path, &c.values)
			if match {
				c.route = route
				c.matched = true
				c.indexHandler = 0
				err := route.Handlers[0](c)
				return true, err
			}
		}
	}

	// 没有找到匹配的路由
	err := NewError(StatusNotFound, c.method+" "+c.path+" Not found")
	if !c.matched && app.methodExist(c) {
		err = ErrMethodNotAllowed
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
