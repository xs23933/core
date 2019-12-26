package core

import (
	"context"
	"net/http"
	"reflect"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/dimfeld/httptreemux"
	"github.com/pelletier/go-toml"
	"github.com/unrolled/render"
)

// TreeMux TreeMux
type TreeMux *httptreemux.TreeMux

// RequestHandler 请求转化对象
type RequestHandler struct {
	prefix string
	Router *httptreemux.TreeMux
}

func (h *RequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Router.ServeHTTP(w, r)
}

// JSON JSON
func (h *RequestHandler) JSON(w http.ResponseWriter, v interface{}, err error) {
	if err != nil {
		render.New().JSON(w, http.StatusOK, map[string]interface{}{
			"status": false,
			"data":   v,
			"msg":    err.Error(),
		})
		return
	}
	render.New().JSON(w, http.StatusOK, map[string]interface{}{
		"status": true,
		"data":   v,
		"msg":    "success",
	})
}

// OriginJSON OriginJSON
func (h *RequestHandler) OriginJSON(w http.ResponseWriter, v interface{}) {
	render.New().JSON(w, http.StatusOK, v)
}

// Handler handler 基类
type Handler struct {
}

// JSON 输出json数据 继承者都可以
func (h *Handler) JSON(w http.ResponseWriter, v interface{}, err error) {
	if err != nil {
		render.New().JSON(w, http.StatusOK, map[string]interface{}{
			"status": false,
			"data":   v,
			"msg":    err.Error(),
		})
		return
	}
	render.New().JSON(w, http.StatusOK, map[string]interface{}{
		"status": true,
		"data":   v,
		"msg":    "success",
	})
}

// OriginJSON 直接输出json 不做任何处理
func (h *Handler) OriginJSON(w http.ResponseWriter, v interface{}) {
	render.New().JSON(w, http.StatusOK, v)
}

// Data 直接输出Data
func (h *Handler) Data(w http.ResponseWriter, v []byte) {
	render.New(render.Options{
		BinaryContentType: "text/json",
	}).Data(w, http.StatusOK, v)
}

// Request 请求request
type Request interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
	PushGet(string, httptreemux.HandlerFunc)
	PushPost(string, httptreemux.HandlerFunc)
	PushPut(string, httptreemux.HandlerFunc)
	PushDelete(string, httptreemux.HandlerFunc)
	InitRouter()
	Init()
	Uri(name, method string) string
}

func (h *RequestHandler) Init() {
	h.prefix = ""
}

// InitRouter 初始化路由
func (h *RequestHandler) InitRouter() {
	if h.Router == nil {
		h.Router = httptreemux.New()
	}
}

// PushGet 注册get
func (h *RequestHandler) PushGet(uri string, handler httptreemux.HandlerFunc) {
	h.Router.GET(uri, handler)
}

// PushPost 注册PushPost
func (h *RequestHandler) PushPost(uri string, handler httptreemux.HandlerFunc) {
	h.Router.POST(uri, handler)
}

// PushPut 注册PushPut
func (h *RequestHandler) PushPut(uri string, handler httptreemux.HandlerFunc) {
	h.Router.PUT(uri, handler)
}

// PushDelete 注册PushDelete
func (h *RequestHandler) PushDelete(uri string, handler httptreemux.HandlerFunc) {
	h.Router.DELETE(uri, handler)
}

func (h *RequestHandler) Uri(name, method string) string {
	return h.prefix + strings.TrimPrefix(name, method)
}

func (h *RequestHandler) SetPrefix(prefix string) {
	h.prefix = prefix
}

// Core core
type Core struct {
	ctx    context.Context
	config *toml.Tree
}

// New 创建 Core
func New(file string) *Core {
	ctx := context.Background()
	return &Core{
		ctx:    ctx,
		config: NewConfig(file),
	}
}

// Run 运行
func (c *Core) Run(hand Request) error {
	handler := c.handleContext(hand)
	port := ":" + c.config.Get("app.port").(string)
	server := &http.Server{Addr: port, Handler: handler}

	Log("Start http://127.0.0.1%s", port)

	return server.ListenAndServe()
}

func (c *Core) handleContext(hand Request) http.Handler {
	hand.InitRouter()
	hand.Init()
	// 注册路由
	refCtl := reflect.TypeOf(hand)
	methodCount := refCtl.NumMethod()
	valFn := reflect.ValueOf(hand)
	Log("Auto register router")
	for idx := 0; idx < methodCount; idx++ {
		m := refCtl.Method(idx)
		name := toNamer(m.Name)
		switch {
		case strings.HasPrefix(name, "get"):
			if fn, ok := (valFn.Method(idx).Interface()).(func(http.ResponseWriter, *http.Request, map[string]string)); ok {
				uri := hand.Uri(name, "get")
				hand.PushGet(uri, fn)
				Log("GET %s", uri)
			}
		case strings.HasPrefix(name, "post"):
			if fn, ok := (valFn.Method(idx).Interface()).(func(http.ResponseWriter, *http.Request, map[string]string)); ok {
				uri := hand.Uri(name, "post")
				hand.PushPost(uri, fn)
				Log("POST %s", uri)
			}
		case strings.HasPrefix(name, "put"):
			if fn, ok := (valFn.Method(idx).Interface()).(func(http.ResponseWriter, *http.Request, map[string]string)); ok {
				uri := hand.Uri(name, "put")
				hand.PushPut(uri, fn)
				Log("PUT %s", uri)
			}
		case strings.HasPrefix(name, "delete"):
			if fn, ok := (valFn.Method(idx).Interface()).(func(http.ResponseWriter, *http.Request, map[string]string)); ok {
				uri := hand.Uri(name, "delete")
				hand.PushDelete(uri, fn)
				Log("DELETE %s", uri)
			}
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hand.ServeHTTP(w, r.WithContext(c.ctx))
	})
}

// Dump 打印数据信息
func Dump(v ...interface{}) {
	spew.Dump(v...)
}
