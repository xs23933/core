package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"

	"github.com/xs23933/core"
)

func main() {
	conf := core.LoadConfigFile("./config.yml")
	app := core.NewEngine(conf)
	// app.Use(core.Logger())
	app.Use(core.NewTextView("./views", ".js", app.Debug))
	app.Get("/foo", func(c *core.Ctx) {
		c.Render("test", core.Map{"IP": "127.0.0.1"})
	})
	app.Get("/bar", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("bar"))
	})
	go func() {
		ip := "0.0.0.0:6060"
		if err := http.ListenAndServe(ip, nil); err != nil {
			fmt.Printf("start pprof failed on %s\n", ip)
		}
	}()
	if err := app.ListenAndServe(); err != nil {
		panic(err)
	}
}
