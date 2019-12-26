# Auto register router

* Prefix Get|Post|Put|Delete Func to auto register router

#### Config.conf
```conf
[app]
port = "8080"
debug = true

```

#### handler.go
```go
import (
  "github.com/xs23933/core"
)

type Handler struct {
  core.RequestHandler
}

// ServeHTTP if check ServeHTTP or hook method
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
		default: // access check required.
			us, err := models.AuthenticateUser(r)
			if err != nil {
				render.New().JSON(w, http.StatusOK, map[string]interface{}{
					"status": false,
					"data":   nil,
					"msg":    "access denied",
				})
				return
			}
			ctx := context.WithValue(r.Context(), "us", us)
			h.Router.ServeHTTP(w, r.WithContext(ctx))
		}
}


// GetHome uri is /home
func (h *Handler) GetHome(w http.ResponseWriter, r *http.Request, params map[string]string) {
  h.OriginJSON(map[string]string{
    "Hello": "world",
  })
}

```


#### main.go
```go
import (
  "github.com/xs23933/core"
)

func main(){
  app := core.New(config)
  hand := &Handler{}

  app.Run(hand)

  if err := app.Run(hand); err != nil {
    core.Log("%v", err.Error())
  }
}
```