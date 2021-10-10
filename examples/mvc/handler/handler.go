package handler

import (
	"github.com/xs23933/core"
)

type Handler struct {
	core.Handler
}

func (h *Handler) Init() {
	h.Prefix("/api")
}

func (Handler) Get(c *core.Ctx) {
	c.SendString("Hello world")
	core.Conn()
}

func (Handler) GetParam1Param2Params(c *core.Ctx) {
	us := struct {
		First, Last string
	}{
		First: "xs",
		Last:  "xs",
	}
	var usr struct {
		First, Last string
	}
	c.Set("us", us)
	c.GetAs("us", &usr)
	c.JSON(usr)
}

type Name struct {
	First, Last string
}

type Person struct {
	Name   Name
	Gender string
	Age    int
}

func (Handler) Post(c *core.Ctx) {
	f, err := c.FormFile("file")
	core.Dump(f, err)
}
