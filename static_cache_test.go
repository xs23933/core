package core

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestStaticCachePolicy(t *testing.T) {
	fs := http.FS(fstest.MapFS{
		"app.js":     {Data: []byte("console.log('ok')"), ModTime: time.Unix(1700000000, 0)},
		"index.html": {Data: []byte("<!doctype html><title>app</title>")},
		"photo.jpg":  {Data: []byte{0xff, 0xd8, 0xff, 0xdb}},
	})
	for _, tt := range []struct {
		name, path, method, cache string
		config                    []StaticConfig
		status                    int
	}{
		{"legacy", "/assets/app.js", "GET", "", nil, 200},
		{"default", "/assets/app.js", "GET", "public, max-age=604800", []StaticConfig{{}}, 200},
		{"head", "/assets/app.js", "HEAD", "public, max-age=604800", []StaticConfig{{}}, 200},
		{"image", "/assets/photo.jpg", "GET", "public, max-age=604800", []StaticConfig{{}}, 200},
		{"html", "/assets/", "GET", "no-cache", []StaticConfig{{Immutable: true}}, 200},
		{"immutable", "/assets/app.js", "GET", "public, max-age=31536000, immutable", []StaticConfig{{MaxAge: 365 * 24 * time.Hour, Immutable: true}}, 200},
		{"disabled", "/assets/app.js", "GET", "no-cache", []StaticConfig{{MaxAge: -1}}, 200},
		{"missing", "/assets/missing.js", "GET", "", []StaticConfig{{}}, 404},
		{"redirect", "/assets/index.html", "GET", "", []StaticConfig{{}}, 301},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := New()
			app.StaticFS("/assets", fs, tt.config...)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.status || rec.Result().Header.Get("Cache-Control") != tt.cache {
				t.Fatalf("status=%d cache=%q, want %d %q", rec.Code, rec.Result().Header.Get("Cache-Control"), tt.status, tt.cache)
			}
			if tt.method == "HEAD" && rec.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
		})
	}
}

func TestStaticCachePreservesExplicitPolicyAndDynamicResponses(t *testing.T) {
	app := New()
	app.Use(func(c Ctx) error {
		if c.Path() == "/assets/private.js" {
			c.SetHeader("Cache-Control", "private, no-store")
		}
		return c.Next()
	})
	app.StaticFS("/assets", http.FS(fstest.MapFS{"private.js": {Data: []byte("secret")}}), StaticConfig{})
	app.GET("/api/value.js", func(c Ctx) error { return c.SendString("dynamic") })
	for _, tt := range []struct{ path, cache string }{
		{"/assets/private.js", "private, no-store"},
		{"/api/value.js", ""},
	} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", tt.path, nil))
		if rec.Code != 200 || rec.Result().Header.Get("Cache-Control") != tt.cache {
			t.Fatalf("%s: status=%d cache=%q", tt.path, rec.Code, rec.Result().Header.Get("Cache-Control"))
		}
	}
}

func TestStaticCacheConditionalAndRange(t *testing.T) {
	app := New()
	app.StaticFS("/assets", http.FS(fstest.MapFS{
		"app.js": {Data: []byte("0123456789"), ModTime: time.Unix(1700000000, 0)},
	}), StaticConfig{})
	for _, tt := range []struct {
		header, value string
		status        int
		body          string
	}{
		{"If-Modified-Since", "Tue, 14 Nov 2023 22:13:20 GMT", 304, ""},
		{"Range", "bytes=2-5", 206, "2345"},
		{"Range", "bytes=100-200", 416, "invalid range: failed to overlap\n"},
	} {
		req := httptest.NewRequest("GET", "/assets/app.js", nil)
		req.Header.Set(tt.header, tt.value)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != tt.status || rec.Body.String() != tt.body {
			t.Fatalf("%s: status=%d body=%q", tt.header, rec.Code, rec.Body.String())
		}
		want := "public, max-age=604800"
		if tt.status == 416 {
			want = ""
		}
		if rec.Result().Header.Get("Cache-Control") != want {
			t.Fatalf("cache=%q, want %q", rec.Result().Header.Get("Cache-Control"), want)
		}
	}
}

func TestStaticCacheAllFileAPIs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('ok')"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, register := range []func(*Core){
		func(app *Core) { app.Static("/assets", dir, StaticConfig{}) },
		func(app *Core) { app.StaticFile("/assets/app.js", filepath.Join(dir, "app.js"), StaticConfig{}) },
		func(app *Core) { app.StaticFileFS("/assets/app.js", "app.js", http.Dir(dir), StaticConfig{}) },
	} {
		app := New()
		register(app)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/app.js", nil))
		if rec.Code != 200 || rec.Body.String() != "console.log('ok')" || rec.Result().Header.Get("Cache-Control") != "public, max-age=604800" {
			t.Fatalf("status=%d body=%q cache=%q", rec.Code, rec.Body.String(), rec.Result().Header.Get("Cache-Control"))
		}
	}
}
