package handler

import (
	"fmt"

	"github.com/xs23933/core"
	"github.com/xs23933/core/examples/mvc/models"
)

// define handler
type Handler struct {
	core.Handler // must extends core.Handler
}

// Put create user
// put /
//
//	{
//	    "user": "username",
//	    "password": "password"
//	}
//
// route: PUT / > main.Handler.Put
func (Handler) Put(c *core.Ctx) {
	form := models.User{}
	// read put post body data
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
// route: GET /:param > main.Handler.GetDetailParam
func (Handler) GetParam(c *core.Ctx) {
	// get param
	uid := c.GetParamUid("param")
	if uid.IsEmpty() {
		c.ToJSON(nil, fmt.Errorf("param invalid"))
		return
	}
	c.ToJSON(models.UserById(uid))
}

// Get get user list
// get /
//
// route: GET / > main.Handler.Get
func (Handler) Get(c *core.Ctx) {
	c.ToJSON(models.UserPage())
}

func init() {
	// auto register handler
	core.RegHandle(new(Handler))
}
