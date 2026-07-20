package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkRouteMatchStatic 基准测试静态路由匹配
func BenchmarkRouteMatchStatic(b *testing.B) {
	app := New()
	app.GET("/api/v1/users", func(c Ctx) error {
		return c.SendString("ok")
	})

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
	}
}

// BenchmarkRouteMatchOneParam 基准测试单参数路由匹配
func BenchmarkRouteMatchOneParam(b *testing.B) {
	app := New()
	app.GET("/users/:id", func(c Ctx) error {
		return c.SendString(c.Params("id"))
	})

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/users/12345", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
	}
}

// BenchmarkRouteMatchMultiParam 基准测试多参数路由匹配
func BenchmarkRouteMatchMultiParam(b *testing.B) {
	app := New()
	app.POST("/items/:category/list", func(c Ctx) error {
		return c.SendString(c.Params("category"))
	})

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/items/electronics/list", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
	}
}

// BenchmarkRouteMatchDeep 基准测试深层嵌套路由
func BenchmarkRouteMatchDeep(b *testing.B) {
	app := New()
	app.GET("/api/v1/orgs/:orgId/teams/:teamId/users/:userId", func(c Ctx) error {
		return c.SendString(c.Params("userId"))
	})

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/teams/eng/users/alice", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
	}
}
