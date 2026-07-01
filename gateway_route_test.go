package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type paramSaveTestHandler struct {
	Handler
}

func (h *paramSaveTestHandler) Init() {
	h.Prefix("/api")
}

func (h *paramSaveTestHandler) PostParamSave(c Ctx) {
	c.SendString(c.Params("param"))
}

func TestRemoveHandle(t *testing.T) {
	app := New()
	app.GET("/gateway/:id", func(c Ctx) error {
		return c.SendString("old")
	})

	req := httptest.NewRequest(http.MethodGet, "/gateway/1", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "old" {
		t.Fatalf("before remove status/body = %d/%q, want 200/old", rec.Code, rec.Body.String())
	}

	app.RemoveHandle([]string{http.MethodGet}, "/gateway/:id")

	req = httptest.NewRequest(http.MethodGet, "/gateway/1", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("after remove status = %d, want 404", rec.Code)
	}

	app.GET("/gateway/:id", func(c Ctx) error {
		return c.SendString("new")
	})

	req = httptest.NewRequest(http.MethodGet, "/gateway/1", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "new" {
		t.Fatalf("after re-add status/body = %d/%q, want 200/new", rec.Code, rec.Body.String())
	}
}

func TestAllMethodRoute(t *testing.T) {
	app := New()
	app.ALL("/gateway/all", func(c Ctx) error {
		return c.SendString(c.Method())
	})

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/gateway/all", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != method {
			t.Fatalf("%s status/body = %d/%q, want 200/%s", method, rec.Code, rec.Body.String(), method)
		}
	}
}

func TestRouteHandlerRegisteredOnce(t *testing.T) {
	app := New()
	calls := 0

	app.GET("/gateway/once", func(c Ctx) error {
		calls++
		return c.Next()
	})

	req := httptest.NewRequest(http.MethodGet, "/gateway/once", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

func TestGroupRouteHandlerRegisteredOnce(t *testing.T) {
	app := New()
	calls := 0

	api := app.Group("/api")
	api.GET("/once", func(c Ctx) error {
		calls++
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/once", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status/body = %d/%q, want 200/ok", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

func TestNestedGroupRoutePrefix(t *testing.T) {
	app := New()

	api := app.Group("/api")
	v1 := api.Group("/v1")
	v1.GET("/users", func(c Ctx) error {
		return c.SendString("users")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "users" {
		t.Fatalf("status/body = %d/%q, want 200/users", rec.Code, rec.Body.String())
	}
}

func TestParamRouteBeforeStaticSegment(t *testing.T) {
	app := New()

	app.POST("/api/:param/save", func(c Ctx) error {
		return c.SendString(c.Params("param"))
	})

	req := httptest.NewRequest(http.MethodPost, "/api/bkash/save", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "bkash" {
		t.Fatalf("status/body = %d/%q, want 200/bkash", rec.Code, rec.Body.String())
	}
}

func TestAutoRouteParamBeforeStaticSegment(t *testing.T) {
	app := New()
	app.addHandler(&paramSaveTestHandler{})

	req := httptest.NewRequest(http.MethodPost, "/api/bkash/save", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "bkash" {
		t.Fatalf("status/body = %d/%q, want 200/bkash", rec.Code, rec.Body.String())
	}
}

func TestParamRouteCanCoexistWithStaticRoute(t *testing.T) {
	app := New()

	app.POST("/api/:param/save", func(c Ctx) error {
		return c.SendString("param-save:" + c.Params("param"))
	})
	app.POST("/api/save/:param", func(c Ctx) error {
		return c.SendString("save-param:" + c.Params("param"))
	})

	req := httptest.NewRequest(http.MethodPost, "/api/bkash/save", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "param-save:bkash" {
		t.Fatalf("param route status/body = %d/%q, want 200/param-save:bkash", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/save/bkash", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "save-param:bkash" {
		t.Fatalf("static route status/body = %d/%q, want 200/save-param:bkash", rec.Code, rec.Body.String())
	}
}

func TestParamRouteFallbackWhenStaticPrefixMisses(t *testing.T) {
	app := New()

	app.POST("/api/bkash/verify", func(c Ctx) error {
		return c.SendString("verify")
	})
	app.POST("/api/:param/save", func(c Ctx) error {
		return c.SendString("save:" + c.Params("param"))
	})

	req := httptest.NewRequest(http.MethodPost, "/api/bkash/verify", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "verify" {
		t.Fatalf("static route status/body = %d/%q, want 200/verify", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/bkash/save", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "save:bkash" {
		t.Fatalf("param fallback status/body = %d/%q, want 200/save:bkash", rec.Code, rec.Body.String())
	}
}

func TestConfiguredPostOnlyMethodStillRegistersPostRoute(t *testing.T) {
	app := New(Options{
		"methods": []string{http.MethodPost},
	})

	app.POST("/api/:param/save", func(c Ctx) error {
		return c.SendString(c.Params("param"))
	})

	req := httptest.NewRequest(http.MethodPost, "/api/bkash/save", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "bkash" {
		t.Fatalf("status/body = %d/%q, want 200/bkash", rec.Code, rec.Body.String())
	}
}

func TestRootCatchAllDoesNotShadowStaticRoute(t *testing.T) {
	app := New()

	app.GET("/*", func(c Ctx) error {
		return c.SendString("catch")
	})
	app.GET("/health", func(c Ctx) error {
		return c.SendString("health")
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "health" {
		t.Fatalf("status/body = %d/%q, want 200/health", rec.Code, rec.Body.String())
	}
}

func TestStaticRouteMatchDoesNotAllocate(t *testing.T) {
	root := &RouteNode{
		path:        "/",
		nType:       root,
		staticChild: make(map[string]*RouteNode),
	}
	root.addRoute("/api/users/list", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := &BaseCtx{handlers: make(HandlerFuncs, 0, 4)}

	handlers, ok := root.match("/api/users/list", ctx)
	if !ok || len(handlers) != 1 {
		t.Fatalf("match handlers = %d/%v, want one handler", len(handlers), ok)
	}

	allocs := testing.AllocsPerRun(1000, func() {
		handlers, ok := root.match("/api/users/list", ctx)
		if !ok || len(handlers) != 1 {
			t.Fatalf("match handlers = %d/%v, want one handler", len(handlers), ok)
		}
	})
	if allocs != 0 {
		t.Fatalf("static route match allocations = %v, want 0", allocs)
	}
}
