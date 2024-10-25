package main

import (
	"github.com/xs23933/core/v2"
	"github.com/xs23933/core/v2/middleware/cros"
	"github.com/xs23933/core/v2/middleware/view/html"
)

func main() {
	app := core.New()

	app.Use(html.NewHtmlView("views", ".html"))

	app.Use(cros.New())

	app.Use(func(c core.Ctx) error {
		return c.Next()
	})

	app.Get("/", func(c core.Ctx) {
		c.SendString("what happend")
	})

	app.Get("/what", func(c core.Ctx) {
		c.ToJSONCode(nil, core.NewError(12312, "asfasdf"))
	})

	app.Get("/md", func(c core.Ctx) {
		c.Render("test")
	})

	if err := app.Listen(8081); err != nil {
		panic(err)
	}
}
