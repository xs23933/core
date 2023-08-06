package main

import (
	"github.com/xs23933/core/v2"
	"github.com/xs23933/core/v2/example/work/models"
	"github.com/xs23933/core/v2/middware/favicon"
	"github.com/xs23933/core/v2/middware/requestid"
)

type Handler struct {
	core.Handler
}

func (Handler) Put(c core.Ctx) {
	form := models.User{}
	if err := c.ReadBody(&form); err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(form, form.Save())
}

// GetParam get some param
//
// get param id planA
// @param: uid.UID userId
// route: GET /detail/:param > main.Handler.GetDetailParam
func (Handler) GetDetailParam(c core.Ctx) {
	uid, err := c.ParamsUid("param")
	if err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(models.UserById(uid))
}

func (Handler) Get_id(c core.Ctx) {
	uid, err := c.ParamsUid("id")
	if err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(models.UserById(uid))
}

func (Handler) Get(c core.Ctx) {
	c.ToJSON(models.UserPage())
}

func init() {
	core.RegHandle(new(Handler))
	// core.RegisterModule(Handler{})
}

func main() {

	app := core.New(core.LoadConfigFile("config.yaml"))
	app.Use(requestid.New())
	app.Use(favicon.New(favicon.Config{
		File: "tmp/favicon.ico",
		Url:  "/favicon.ico",
	}))
	models.InitDB()
	app.Listen()
}
