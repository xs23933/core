package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var benchmarkServeHTTPParam string

type benchmarkResponseWriter struct {
	header http.Header
}

func newBenchmarkResponseWriter() *benchmarkResponseWriter {
	return &benchmarkResponseWriter{header: make(http.Header)}
}

func (w *benchmarkResponseWriter) Header() http.Header            { return w.header }
func (w *benchmarkResponseWriter) WriteHeader(int)                {}
func (w *benchmarkResponseWriter) Write(body []byte) (int, error) { return len(body), nil }

// BenchmarkServeHTTPStatic 测量完整请求链，但排除每轮构造 httptest Request/Recorder 的成本。
func BenchmarkServeHTTPStatic(b *testing.B) {
	app := New(Options{"debug": false})
	app.GET("/api/v1/users", func(Ctx) error { return nil })
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	writer := newBenchmarkResponseWriter()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		app.ServeHTTP(writer, request)
	}
}

func BenchmarkServeHTTPOneParam(b *testing.B) {
	app := New(Options{"debug": false})
	app.GET("/users/:id", func(c Ctx) error {
		benchmarkServeHTTPParam = c.Params("id")
		return nil
	})
	request := httptest.NewRequest(http.MethodGet, "/users/12345", nil)
	writer := newBenchmarkResponseWriter()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		app.ServeHTTP(writer, request)
	}
}

func BenchmarkServeHTTPMultiParam(b *testing.B) {
	app := New(Options{"debug": false})
	app.POST("/orgs/:orgId/teams/:teamId/users/:userId", func(c Ctx) error {
		benchmarkServeHTTPParam = c.Params("userId")
		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/orgs/acme/teams/eng/users/alice", nil)
	writer := newBenchmarkResponseWriter()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		app.ServeHTTP(writer, request)
	}
}

func BenchmarkServeHTTPDeep(b *testing.B) {
	app := New(Options{"debug": false})
	app.GET("/api/v1/orgs/:orgId/teams/:teamId/users/:userId", func(c Ctx) error {
		benchmarkServeHTTPParam = c.Params("userId")
		return nil
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/teams/eng/users/alice", nil)
	writer := newBenchmarkResponseWriter()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		app.ServeHTTP(writer, request)
	}
}
