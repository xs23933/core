package main

import (
	"github.com/xs23933/core/examples/mvc/handler"

	"github.com/xs23933/core"
)

func main() {
	app := core.Default(core.LoadConfigFile("config.yml"))
	app.Use(new(handler.Handler))
	if err := app.ListenAndServe(); err != nil {
		panic(err)
	}
}
