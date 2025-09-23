package core

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
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
	case []HandlerFuncs:
		for _, v := range h {
			hands = append(hands, v...)
		}
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

	for _, method := range methods {
		method := strings.ToUpper(method)
		if err := app.validateMethod(method); err != nil {
			panic(err)
		}

		if method == MethodUse {
			if uri == "/" || uri == "" { // 全局中间件
				for _, root := range app.trees {
					root.middlewares = append(handlers, root.middlewares...)
				}
			} else {
				for _, root := range app.trees {
					node := root.addRouteNode(uri)
					node.middlewares = append(node.middlewares, handlers...)
				}
			}
			continue
		}

		root := app.trees[methodPos(method)]

		root.addRoute(uri, handlers)
	}
	return app
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

func (app *Core) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, ok := app.AcquireCtx(w, r).(*BaseCtx)
	if !ok {
		panic("field to type-assert to Ctx")
	}
	defer app.ReleaseCtx(c)

	method := c.Method()
	root := app.trees[methodPos(method)]
	Info("match %s => %s", method, c.Path())
	handlers, ok := root.match(c.Path(), c)
	if !ok {
		c.SendString(ErrNotFound)
		return
	}

	c.handlers = handlers
	c.indexHandler = -1
	if err := c.Next(); err != nil {
		if e, ok := err.(Errors); ok {
			c.SendStatus(e.Errors())
		}
	}
}
