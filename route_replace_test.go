package core

import (
	"net/http/httptest"
	"testing"
)

func TestReplaceHandlePreservesInFlightAndRenamesParameters(t *testing.T) {
	app := New(Options{"debug": false})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app.GET("/items/:id", func(c Ctx) error { close(entered); <-release; return c.SendString("old:" + c.Params("id")) })
	old := httptest.NewRecorder()
	go func() { app.ServeHTTP(old, httptest.NewRequest("GET", "/items/1", nil)); close(done) }()
	<-entered
	app.ReplaceHandle([]string{"GET"}, "/items/:id", "/items/:name", func(c Ctx) error { return c.SendString("new:" + c.Params("name")) })
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/items/2", nil))
	close(release)
	<-done
	if old.Body.String() != "old:1" || rec.Body.String() != "new:2" {
		t.Fatal(old.Body, rec.Body)
	}
	app.ReplaceHandle([]string{"GET"}, "/items/:name", "/new/:name", func(c Ctx) error { return c.SendString(c.Params("name")) })
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/items/3", nil))
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/new/3", nil))
	if rec.Body.String() != "3" {
		t.Fatal(rec.Body)
	}
}

func TestReplaceHandleMethodsAndCase(t *testing.T) {
	app := New(Options{"debug": false, "case-sensitive": false})
	app.ALL("/OLD", func(c Ctx) error { return c.SendString("old") })
	app.ReplaceHandle([]string{MethodAll}, "/old", "/New", func(c Ctx) error { return c.SendString("new") }, func(c Ctx) error { c.SetHeader("X-Middleware", "yes"); return c.Next() })
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(method, "/NEW", nil))
		if rec.Body.String() != "new" || rec.Header().Get("X-Middleware") != "yes" {
			t.Fatal(method, rec.Body, rec.Header())
		}
		rec = httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(method, "/old", nil))
		if rec.Code != 404 {
			t.Fatal(method, rec.Code)
		}
	}
}
