package core

import (
	"context"
	"net/http"

	"github.com/davecgh/go-spew/spew"
	"github.com/dimfeld/httptreemux"
	"github.com/pelletier/go-toml"
	"github.com/unrolled/render"
)

// TreeMux TreeMux
type TreeMux *httptreemux.TreeMux

// RequestHandler 请求转化对象
type RequestHandler struct {
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

func (c *Core) handleContext(hand http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hand.ServeHTTP(w, r.WithContext(c.ctx))
	})
}

// Dump 打印数据信息
func Dump(v ...interface{}) {
	spew.Dump(v...)
}
