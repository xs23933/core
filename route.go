package core

import (
	"fmt"
	"net/http"
	"reflect"
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
	Group(prefix string, handlers ...HandlerFuncs) Router
}

func (app *Core) ProcessedHandler(hand any) HandlerFuncs {
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
			has = append(has, app.ProcessedHandler(v)...)
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
	if method != MethodUse && method != MethodAll && methodPos(method) == -1 {
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

/*
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

*/

func (app *Core) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := app.AcquireCtx(w, r)
	defer app.ReleaseCtx(c)

	method := c.Method()
	methodIdx := methodPos(method)

	var root *RouteNode
	if methodIdx >= 0 && methodIdx < len(app.trees) {
		root = app.trees[methodIdx]
	}

	var (
		handlers         HandlerFuncs
		fallbackHandlers HandlerFuncs
		ok               bool
	)
	if root != nil {
		app.routeLocks[methodIdx].RLock()
		handlers, ok = root.match(c.Path(), c)
		if !ok {
			fallbackHandlers = append(c.handlers[:0], root.middlewares...)
		}
		app.routeLocks[methodIdx].RUnlock()
	}

	if !ok {
		if root == nil && len(app.trees) > 0 && app.trees[0] != nil {
			app.routeLocks[0].RLock()
			fallbackHandlers = append(c.handlers[:0], app.trees[0].middlewares...)
			app.routeLocks[0].RUnlock()
		}
		handlers = fallbackHandlers
		handlers = append(handlers, func(c Ctx) error {
			return c.SendStatus(StatusNotFound, ErrNotFound.Error())
		})
	} else {
		c.matched = true
	}

	c.handlers = handlers
	c.indexHandler = -1
	if err := c.Next(); err != nil {
		code, message := errorStatus(err)
		c.SendStatus(code, message)
	}
}

func (app *Core) mutateRoute(methodIndex int, mutate func(*RouteNode)) {
	if methodIndex < 0 || methodIndex >= len(app.trees) || app.trees[methodIndex] == nil {
		return
	}
	app.routeLocks[methodIndex].Lock()
	defer app.routeLocks[methodIndex].Unlock()
	mutate(app.trees[methodIndex])
}

// core.go - AddHandle 方法
func (app *Core) AddHandle(methods []string, uri string, group *Group, handler any, middleware ...HandlerFunc) Router {
	// 构建处理器链
	var handlers HandlerFuncs

	// 添加中间件
	if len(middleware) > 0 {
		handlers = append(handlers, middleware...)
	}

	// 添加主处理器
	if handler != nil {
		handlers = append(handlers, app.ProcessedHandler(handler)...)
	}

	if len(handlers) == 0 {
		panic(fmt.Sprintf("missing handler/middleware in route: %s\n", uri))
	}

	// 确保路径格式
	uri = app.preparePath(uri)
	// 在获取写锁前完成格式校验，避免注册 panic 后遗留锁状态。
	_ = splitPath(uri)

	for _, method := range methods {
		method := strings.ToUpper(method)
		if err := app.validateMethod(method); err != nil {
			panic(err)
		}

		if method == MethodUse {
			if uri == "/" || uri == "" { // 全局中间件
				for index := range app.trees {
					app.mutateRoute(index, func(root *RouteNode) {
						root.middlewares = append(handlers, root.middlewares...)
					})
				}
			} else {
				for index := range app.trees {
					app.mutateRoute(index, func(root *RouteNode) {
						node := root.addRouteNode(uri)
						node.middlewares = append(node.middlewares, handlers...)
					})
				}
			}
			continue
		}

		if method == MethodAll {
			for i := range app.trees {
				app.mutateRoute(i, func(root *RouteNode) {
					root.addRoute(uri, handlers)
				})
			}
			continue
		}

		methodIdx := methodPos(method)
		app.mutateRoute(methodIdx, func(root *RouteNode) {
			root.addRoute(uri, handlers)
		})
	}
	return app
}

func (app *Core) RemoveHandle(methods []string, uri string) {
	uri = app.preparePath(uri)
	_ = splitPath(uri)

	for _, method := range methods {
		method = strings.ToUpper(method)
		if method == MethodAll {
			for index := range app.trees {
				app.mutateRoute(index, func(root *RouteNode) {
					root.removeRoute(uri)
				})
			}
			continue
		}

		methodIdx := methodPos(method)
		app.mutateRoute(methodIdx, func(root *RouteNode) {
			root.removeRoute(uri)
		})
	}
}
