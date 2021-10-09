package main

import (
	"core"
	"fmt"
	"net/http"
	_ "net/http/pprof"
)

func main() {
	conf := core.LoadConfigFile("./config.yml")
	app := core.New(conf)
	// app.Use(core.Logger())
	app.Get("/foo", func(c *core.Ctx) {
		c.SendString("/foo")
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
