package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xs23933/core/v3"
)

func TestMetricsMiddlewareAndEndpoint(t *testing.T) {
	app := core.New()
	m, mw := New()
	app.Use(mw)
	Mount(app, "/metrics", m)

	app.GET("/users/:id", func(c core.Ctx) error {
		c.Status(http.StatusCreated)
		return c.SendString("created")
	})

	req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `core_http_requests_total{method="GET",path="/users/:id",status="201"} 1`) {
		t.Fatalf("metrics body missing users counter: %s", body)
	}
	if !strings.Contains(body, "core_http_request_duration_seconds_sum") {
		t.Fatalf("metrics body missing duration sum")
	}
}
