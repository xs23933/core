package main

import (
	"github.com/xs23933/core/v2"
	"github.com/xs23933/core/v2/middleware/cros"
	"github.com/xs23933/core/v2/middleware/view/html"
)

func main() {
	app := core.New()

	app.Use(html.NewHtmlView("views", ".html"))

	app.Use(cros.New(app))

	app.Use(func(c core.Ctx) error {
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
	api.GET("/test", func(c core.Ctx) {
		c.SendString("test")
	})
	api.POST("test2", func(c core.Ctx) {
		c.SendString("what happend post")
	})

	if err := app.Listen(8081); err != nil {
		panic(err)
	}
}
