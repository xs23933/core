package core

import (
	"fmt"
	"net/http"
	"reflect"
)

type Core struct {
	Middlewares []any
}

type HandlerFunc = func(Ctx) error
type HandlerNormal = func(w http.ResponseWriter, r *http.Request)

func (c *Core) Listen(port ...any) error {
	addr := ":8000"
	if len(port) > 0 {
		switch v := port[0].(type) {
		case int, uint, int16, int32:
			addr = fmt.Sprintf(":%d", v)
		case string:
			addr = v
		}
	}

	fmt.Println(addr)
	return nil
}

func (c *Core) Use(fn ...any) *Core {
	var (
		prefix   string
		prefixes []string
		handlers = make([]any, 0)
	)
	for _, v := range fn {
		switch arg := v.(type) {
		case string:
			prefix = arg
		case []string:
			prefixes = arg
		case HandlerFunc, HandlerNormal, http.Handler:
			c.Middlewares = append(c.Middlewares, arg)
		default:
			panic(fmt.Sprintf("use: invalid middware %v\n", reflect.TypeOf(arg)))
		}
	}

	if len(prefixes) == 0 {
		prefixes = append(prefixes, prefix)
	}

	for _, prefix := range prefixes {
		c.AddHandle([]string{methodUse}, prefix, handlers...)
	}

	return c
}

func (c *Core) AddHandle(method []string, path string, handler ...any) {

}

func New(options ...Options) *Core {
	c := &Core{}
	return c
}
