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

func (app *Core) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, ok := app.AcquireCtx(w, r).(*BaseCtx)
	if !ok {
		panic("field to type-assert to Ctx")
	}
	defer app.ReleaseCtx(c)

	method := c.Method()
	root := app.trees[methodPos(method)]
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
