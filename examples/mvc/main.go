package main

import (
	"os"

	"github.com/xs23933/core/examples/mvc/handler"

	"github.com/xs23933/core"

	"github.com/mattn/go-isatty"
)

func main() {
	app := core.New(core.LoadConfigFile("config.yml"))
	core.Dump(isatty.IsTerminal(os.Stdout.Fd()))
	app.Use(new(handler.Handler))
	if err := app.ListenAndServe(); err != nil {
		panic(err)
	}
}
