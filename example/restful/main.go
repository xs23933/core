package main

import (
	"github.com/xs23933/core/v2"
	"github.com/xs23933/core/v2/middleware/cros"
)

func main() {
	app := core.New()

	app.Use(cros.New())

	app.Use(func(c core.Ctx) error {
		c.SendString("preload")
		return c.Next()
	})

	app.Get("/", func(c core.Ctx) {
		c.SendString("what happend")
	})

	app.Get("/what", func(c core.Ctx) {
		c.SendString("fuck man body")
	})

	if err := app.Listen(8080); err != nil {
		panic(err)
	}
}
