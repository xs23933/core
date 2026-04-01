package main

import (
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/websocket"
)

func main() {
	app := core.New()
	app.GET("/ws", websocket.New().
		OnConnect(func(c *websocket.Conn) {
			c.Send([]byte("Welcome to WebSocket"))
		}).
		OnMessage(
			func(c *websocket.Conn, mt websocket.MessageType, b []byte) {
				c.SendWithType(mt, b)
				core.Info("Received message: %s", string(b))
			},
		).OnClose(func(c *websocket.Conn) {
		println("Connection closed")
	}).Handler())

	app.GET("/", func(c core.Ctx) {
		c.JSON(core.Map{
			"hello": "world",
		})
	})
	app.Run()
}
