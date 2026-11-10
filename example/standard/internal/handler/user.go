package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/xs23933/core/v3"
	domain "github.com/xs23933/core/v3/example/standard/internal/domain/user"
	"github.com/xs23933/core/v3/example/standard/internal/services"
)

type UserHandler struct {
	core.Handler
	service *services.UserService
}

func NewUserHandler(service *services.UserService) *UserHandler {
	return &UserHandler{service: service}
}
func (h *UserHandler) Init() { h.Prefix("/api/v1/users") }

func responseError(c core.Ctx, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		c.Status(http.StatusNotFound)
		return core.NewError(http.StatusNotFound, err.Error())
	case errors.Is(err, domain.ErrInvalidQuery):
		c.Status(http.StatusBadRequest)
		return core.NewError(http.StatusBadRequest, err.Error())
	default:
		c.Status(http.StatusInternalServerError)
		core.Erro("user request failed: %v", err)
		return core.NewError(http.StatusInternalServerError, "internal server error")
	}
}

func (h *UserHandler) Get(c core.Ctx) error {
	page, err := strconv.Atoi(c.Query("p", "1"))
	if err != nil {
		return c.ToJSON(nil, responseError(c, domain.ErrInvalidQuery))
	}
	size, err := strconv.Atoi(c.Query("l", "20"))
	if err != nil {
		return c.ToJSON(nil, responseError(c, domain.ErrInvalidQuery))
	}
	result, err := h.service.List(c.Ctx(), page, size)
	return c.ToJSON(result, responseError(c, err))
}

func (h *UserHandler) Get_id(c core.Ctx) error {
	result, err := h.service.Get(c.Ctx(), c.Params("id"))
	return c.ToJSON(result, responseError(c, err))
}
