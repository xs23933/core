package main

import (
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/middleware/cros"
	"github.com/xs23933/core/v3/middleware/view/html"
)

func main() {
	app := core.New()

	app.Use(html.NewHtmlView("views", ".html"))

	app.Use(cros.New())

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

	if err := app.Listen(8081); err != nil {
		panic(err)
	}
}

type handler struct {
	core.Handler
}

func (h *handler) Init() {
	core.Dump("init")
}

func (handler) GetHello(c core.Ctx) {
	c.SendString("ok")
}
