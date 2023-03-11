# Web framework

* golang workframe
* support router
* auto register router
* micro serve
* easy run
* support go module go.mod

more doc 

https://godoc.org/github.com/xs23933/core

examples

```go

package main

import (
	"github.com/xs23933/web"
)

// handler.go
type Handler struct {
    web.Handler
}

// Init Init
func (h *Handler) Init() {
	h.SetPrefix("/api") // Add prefix
}

// Get get method
// get /api/
func (Handler) Get(c *web.Ctx) {
    c.Send("Hello world")
}

// GetParams Get something params
//  get /api/:param?
func (Handler) GetParams(c *core.Ctx) {
    c.Send("Param: ", c.Params("param"))
}

// PostParam PostParam
func (Handler) PostParam(c *core.Ctx) {
    form := make(map[string]interface{}, 0)  // or some struct
    c.ReadBody(&form)
	c.Send("Param ", c.Params("param"))
}

// PutParams 可选参数
// put /:param?
func (Handler) PutParams(c *core.Ctx) {
    form := make(map[string]interface{}, 0)  // or some struct
    c.ReadBody(&form)
	c.Send("Param? ", c.Params("param"))
}

// main.go
func main(){
    app := core.NewEngine(&core.LoadConfigFile("./config.yml"))

    app.Use(new(Handler))
    // Serve(port) 
    if err := app.Serve(80); err != nil {
        panic(err)
    }
}


go run example/main.go

+ ---- Auto register router ---- +
| GET	/api/
| GET	/api/:param?
| POST	/api/:param
| PUT	/api/:param?
+ ------------------------------ +
Started server on 0.0.0.0:80
```