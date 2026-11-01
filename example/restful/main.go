package main

import (
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/middleware/cors"
	"github.com/xs23933/core/v3/middleware/requestid"
	"github.com/xs23933/core/v3/middleware/view/html"
)

func main() {
	app := core.New()

	app.Use(html.NewHtmlView("views", ".html"))

	app.Use(cors.New())

	app.Use(requestid.New())

	app.Use(func(c core.Ctx) error {
		core.Erro("Fuck men")
		return c.Next()
	})

	app.Use(func(c core.Ctx) error {
		core.Erro("Fuck men2")
		return c.Next()
	})

	app.Use(func(c core.Ctx) error {
		core.Erro("Fuck men3")
		return c.Next()
	})

	app.GET("/", func(c core.Ctx) {
		c.SendString("what happend")
	})

	app.GET("/what", func(c core.Ctx) {
		c.ToJSONCode(nil, core.NewError(12312, "asfasdf"))
	})

	app.GET("/md", func(c core.Ctx) {
		c.Render("test")
	})

	app.POST("/test", func(c core.Ctx) {
		c.SendString("what happend post 1")
	})
	api := app.Group("/api")
	api.GET("/test/:id", func(c core.Ctx) {
		id := c.Params("id")
		core.Info(id)
		c.Format(id)
	})
	api.POST("test2", func(c core.Ctx) {
		c.SendString("what happend post")
	})

	core.RegHandle(&handler{})

	if err := app.Listen(8080); err != nil {
		panic(err)
	}
}

type handler struct {
	core.Handler
}

// app 启动首先执行
func (h *handler) Init() {
	core.Info("init")
}

// 同步完成依赖准备后才允许发布 ready；使用 eng.Ctx 传递取消。
func (h *handler) Prepare(eng *core.Core) error {
	core.Info("prepare")
	return nil
}

// Prepare 完成后异步执行；长期任务必须响应 eng.Ctx。
func (h *handler) Start(eng *core.Core) error {
	core.Info("start")
	return nil
}

// app 关机执行
func (h *handler) Stop(eng *core.Core) error {
	core.Info("shutdown")
	return nil
}

// 每个请求 都会调用 Preload
func (h *handler) Preload(c core.Ctx) error {
	core.Info("preload")
	return c.Next()
}

func (handler) GetHello(c core.Ctx) {
	c.SendString("ok")
}

func (handler) GetUser_id(c core.Ctx) {
	c.SendString("id is %s", c.Params("id"))
}
