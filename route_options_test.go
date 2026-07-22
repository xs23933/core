package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnmatchedOptionsRunsGlobalMiddleware(t *testing.T) {
	app := New()
	called := false
	app.Use(func(c Ctx) error {
		called = true
		if !IsRouteFallback(c) {
			t.Fatal("unmatched OPTIONS was not marked as a route fallback")
		}
		c.Status(StatusNoContent)
		c.Response().DoWriteHeader()
		return nil
	})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/users", nil))

	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("called=%v status=%d, want true/%d", called, rec.Code, http.StatusNoContent)
	}
}

func TestUnmatchedOptionsWithoutHandlerRemainsNotFound(t *testing.T) {
	app := New()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/users", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestUnmatchedGetIsNotOptionsFallback(t *testing.T) {
	app := New()
	app.Use(func(c Ctx) error {
		if IsRouteFallback(c) {
			t.Fatal("unmatched GET was marked as an OPTIONS route fallback")
		}
		return c.Next()
	})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestExplicitOptionsRouteHasPriority(t *testing.T) {
	app := New()
	app.Use(func(c Ctx) error {
		if IsRouteFallback(c) {
			c.Status(StatusNoContent)
			c.Response().DoWriteHeader()
			return nil
		}
		return c.Next()
	})
	app.OPTIONS("/users", func(c Ctx) error {
		c.Status(StatusAccepted)
		c.Response().DoWriteHeader()
		return nil
	})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/users", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusAccepted)
	}
}
