package main

import (
	_ "github.com/xs23933/core/examples/mvc/handler"
	"github.com/xs23933/core/examples/mvc/models"

	"github.com/xs23933/core"
)

func main() {
	app := core.NewEngine(core.LoadConfigFile("config.yaml"))
	models.InitDB()

	if err := app.ListenAndServe(); err != nil {
		panic(err)
	}
}
