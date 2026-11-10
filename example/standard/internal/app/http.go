package app

import (
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/example/standard/internal/handler"
	"github.com/xs23933/core/v3/example/standard/internal/services"
	"github.com/xs23933/core/v3/middleware/requestid"
)

func register(engine *core.Core, svc *services.UserService) error {
	engine.Use(requestid.New())
	core.RegHandle(handler.NewUserHandler(svc))
	return nil
}
