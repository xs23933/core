package main

import "github.com/xs23933/core/v2"

func main() {
	option := core.Options{}
	app := core.New(option)
	if err := app.Listen(8080); err != nil {
		panic(err)
	}
}
