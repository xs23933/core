package core

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

type Group struct {
	*Core
	parent *Group
	name   string
	Prefix string
}

func (g *Group) Name(name string) Router {
	g.Core.mutex.Lock()
	defer g.Core.mutex.Unlock()

	if g.parent != nil {
		g.name = g.parent.name + name
	} else {
		g.name = name
	}

	return g
}

func (g *Group) core() *Core {
	return g.Core
}

// Use registers a middleware route that will match requests
// with the provided prefix (which is optional and defaults to "/").
// Also, you can pass another app instance as a sub-router along a routing path.
// It's very useful to split up a large API as many independent routers and
// compose them as a single service using Use. The core error handler and
// any of the core sub apps are added to the application's error handlers
// to be invoked on errors that happen within the prefix route.
//
//		app.Use(func(c core.Ctx) error {
//		     return c.Next()
//		})
//		app.Use("/api", func(c core.Ctx) error {
//		     return c.Next()
//		})
//		app.Use("/api", handler, func(c core.Ctx) error {
//		     return c.Next()
//		})
//	 	subApp := core.New()
//		app.Use("/mounted-path", subApp)
//
// This method will match all HTTP verbs: GET, POST, PUT, HEAD etc...
func (g *Group) Use(fn ...any) Router {
	var (
		prefix   string
		prefixes []string
		handlers []any
	)
	for i := range fn {
		switch arg := fn[i].(type) {
		case string:
			prefix = arg
		case []string:
			prefixes = arg
		case HandlerFun, HandlerFunc, http.HandlerFunc, http.Handler:
			handlers = append(handlers, arg)
		case HandlerFuncs:
			handlers = append(handlers, arg)
		default:
			panic(fmt.Sprintf("use: invalid middware %v\n", reflect.TypeOf(arg)))
		}
	}

	if len(prefixes) == 0 {
		prefixes = append(prefixes, prefix)
	}
	for _, prefix := range prefixes {
		g.Core.AddHandle([]string{MethodUse}, getGroupPath(g.Prefix, prefix), g, nil, g.Core.processedHandler(handlers)...)
	}
	return g
}

// Get registers a route for GET methods that requests a representation
// of the specified resource. Requests using GET should only retrieve data.
func (g *Group) Get(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodGet}, path, handler, middleware...)
}

func (g *Group) Head(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodHead}, path, handler, middleware...)
}

func (g *Group) Post(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodPost}, path, handler, middleware...)
}
func (g *Group) Put(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodPut}, path, handler, middleware...)
}
func (g *Group) Delete(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodDelete}, path, handler, middleware...)
}
func (g *Group) Connect(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodConnect}, path, handler, middleware...)
}
func (g *Group) Options(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodOptions}, path, handler, middleware...)
}
func (g *Group) Trace(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodTrace}, path, handler, middleware...)
}
func (g *Group) Patch(path string, handler any, middleware ...any) Router {
	return g.Add([]string{MethodPatch}, path, handler, middleware...)
}

// Add allows you to specify multiple HTTP methods to register a route.
func (g *Group) Add(methods []string, path string, handler any, middleware ...any) Router {
	handlers := middleware
	if handler != nil {
		handlers = append(handlers, handler)
	}
	uri := getGroupPath(g.Prefix, path)
	D("route: %s %s", strings.Join(methods, ","), uri)
	return g.Core.AddHandle(methods, uri, g, handler, g.Core.processedHandler(handlers)...)
}
