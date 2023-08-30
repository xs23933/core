package main

import (
	"github.com/xs23933/core/v2"
	"github.com/xs23933/core/v2/example/work/models"
	"github.com/xs23933/core/v2/middleware/requestid"
	"github.com/xs23933/core/v2/middleware/view"
	"github.com/xs23933/core/v2/middleware/view/html"
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
	uid, err := c.ParamsUuid("param")
	if err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(models.UserById(uid))
}

func (Handler) GetDetail(c core.Ctx) {
	usid := c.FormValue("id")
	id, err := core.UUIDFromString(usid)
	if err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(models.UserById(id))
}

func (Handler) Get_id(c core.Ctx) {
	uid, err := c.ParamsUuid("id")
	if err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(models.UserById(uid))
}

func (Handler) Get(c core.Ctx) {
	// c.ToJSON(models.UserPage())
	// c.Type("json")
	c.Render("index", core.Map{
		"success": true,
		"msg":     "success",
		"data":    "good",
	})
}

func init() {
	core.RegHandle(new(Handler))
	// core.RegisterModule(Handler{})
}

func main() {

	app := core.New(core.LoadConfigFile("config.yaml"))
	var view view.IEngine = html.NewHtmlView("./views", ".html", app.Debug)
	app.Use(view)
	app.Use(requestid.New())
	// models.InitDB()
	app.Listen()
}
