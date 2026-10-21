package gzip_test

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xs23933/core/v3"
	compress "github.com/xs23933/core/v3/middleware/gzip"
)

func TestNegotiationAndContentTypes(t *testing.T) {
	body := strings.Repeat("compressible content ", 100)
	for _, tt := range []struct {
		name, accept, contentType string
		compressed                bool
	}{
		{"json", "gzip", "application/json; charset=utf-8", true},
		{"html", "gzip", "text/html", true},
		{"css", "gzip", "text/css", true},
		{"js", "gzip", "text/javascript", true},
		{"legacy js", "gzip", "application/javascript", true},
		{"svg", "gzip", "image/svg+xml", true},
		{"sniff text", "gzip", "", true},
		{"absent", "", "application/json", false},
		{"unsupported", "br", "application/json", false},
		{"disabled", "gzip;q=0, *;q=1", "application/json", false},
		{"positive quality", "br, gzip;q=0.5", "application/json", true},
		{"wildcard", "*;q=0.5", "application/json", true},
		{"invalid quality", "gzip;q=oops", "application/json", false},
		{"png", "gzip", "image/png", false},
		{"webp", "gzip", "image/webp", false},
		{"jpeg", "gzip", "image/jpeg", false},
		{"sse", "gzip", "text/event-stream", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := core.New()
			app.Use(compress.New())
			app.GET("/", func(c core.Ctx) error {
				if tt.contentType != "" {
					c.SetHeader("Content-Type", tt.contentType)
				}
				c.SetHeader("Content-Length", strconv.Itoa(len(body)))
				return c.SendString(body)
			})
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", tt.accept)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			assertBody(t, rec, tt.compressed, body)
			if tt.compressed && rec.Result().Header.Get("Content-Length") != "" {
				t.Fatal("stale Content-Length")
			}
			if rec.Result().Header.Get("Vary") != "Accept-Encoding" {
				t.Fatalf("Vary=%q", rec.Result().Header.Get("Vary"))
			}
		})
	}
}

func assertBody(t *testing.T, rec *httptest.ResponseRecorder, compressed bool, want string) {
	t.Helper()
	var reader io.Reader = rec.Body
	if compressed {
		if rec.Result().Header.Get("Content-Encoding") != "gzip" {
			t.Fatalf("Content-Encoding=%q", rec.Result().Header.Get("Content-Encoding"))
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		reader = zr
	} else if rec.Result().Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("unexpected gzip")
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("body len=%d, want %d", len(got), len(want))
	}
}

func TestConfigurationAndThreshold(t *testing.T) {
	for _, tt := range []struct {
		name, contentType, body string
		cfg                     compress.Config
		compressed              bool
	}{
		{"small", "text/plain", "small", compress.Config{}, false},
		{"threshold", "text/plain", strings.Repeat("a", 1024), compress.Config{}, true},
		{"custom type", "application/custom", "custom", compress.Config{ContentTypes: []string{"application/custom"}, MinLength: 1, Level: gzip.BestSpeed}, true},
		{"replace defaults", "text/html", strings.Repeat("a", 2048), compress.Config{ContentTypes: []string{"application/custom"}}, false},
		{"skip", "text/plain", strings.Repeat("a", 2048), compress.Config{Skip: func(core.Ctx) bool { return true }}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := core.New()
			app.Use(compress.New(tt.cfg))
			app.GET("/", func(c core.Ctx) error {
				c.SetHeader("Content-Type", tt.contentType)
				// Multiple writes exercise threshold buffering and writer accounting.
				for _, b := range []byte(tt.body) {
					if _, err := c.Response().Write([]byte{b}); err != nil {
						return err
					}
				}
				return nil
			})
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			assertBody(t, rec, tt.compressed, tt.body)
		})
	}
}

func TestBypassAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name, method, header, value string
		status                      int
		body                        string
	}{
		{"head", "HEAD", "", "", 200, ""},
		{"no content", "GET", "", "", 204, ""},
		{"not modified", "GET", "", "", 304, ""},
		{"range", "GET", "Content-Range", "bytes 0-4/10", 206, "12345"},
		{"encoded", "GET", "Content-Encoding", "br", 200, "encoded"},
		{"no transform", "GET", "Cache-Control", "public, no-transform", 200, "original"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := core.New()
			app.Use(compress.New(compress.Config{MinLength: 1}))
			app.ALL("/", func(c core.Ctx) error {
				c.SetHeader("Content-Type", "text/plain")
				if tt.header != "" {
					c.SetHeader(tt.header, tt.value)
				}
				c.Status(tt.status)
				if tt.body == "" {
					c.Response().DoWriteHeader()
					return nil
				}
				return c.SendString(tt.body)
			})
			req := httptest.NewRequest(tt.method, "/", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status=%d", rec.Code)
			}
			assertBody(t, rec, false, tt.body)
		})
	}
	t.Run("error before write", func(t *testing.T) {
		app := core.New()
		app.Use(compress.New())
		app.GET("/", func(core.Ctx) error { return core.NewError(400, "bad input") })
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("status=%d", rec.Code)
		}
		assertBody(t, rec, false, "bad input")
	})
}

func TestFlushAndStaticCacheIntegration(t *testing.T) {
	t.Run("flush", func(t *testing.T) {
		app := core.New()
		app.Use(compress.New(compress.Config{MinLength: 1}))
		app.GET("/", func(c core.Ctx) error {
			if err := c.SendString("first"); err != nil {
				return err
			}
			c.Response().Flush()
			return c.SendString("second")
		})
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		assertBody(t, rec, true, "firstsecond")
		if !rec.Flushed {
			t.Fatal("flush was not forwarded")
		}
	})
	t.Run("static", func(t *testing.T) {
		body := strings.Repeat("const value = 123;", 100)
		app := core.New()
		app.Use(compress.New())
		app.StaticFS("/assets", http.FS(fstest.MapFS{"app.js": {Data: []byte(body), ModTime: time.Unix(1700000000, 0)}}), core.StaticConfig{MaxAge: 365 * 24 * time.Hour, Immutable: true})
		req := httptest.NewRequest("GET", "/assets/app.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		assertBody(t, rec, true, body)
		if rec.Result().Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatal(rec.Header())
		}
	})
}

func TestWriterFailurePropagates(t *testing.T) {
	sentinel := errors.New("write failed")
	app := core.New()
	var got error
	// This outer middleware observes errors returned by gzip, including Close.
	app.Use(compress.New(compress.Config{MinLength: 1}))
	app.Use(func(c core.Ctx) error { got = c.Next(); return nil })
	app.GET("/", func(c core.Ctx) error { return c.SendString("compress me") })
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	app.ServeHTTP(&failingWriter{header: make(http.Header), err: sentinel}, req)
	if !errors.Is(got, sentinel) {
		t.Fatalf("error=%v", got)
	}
}

func TestErrorAfterCompressedWrite(t *testing.T) {
	app := core.New()
	app.Use(compress.New(compress.Config{MinLength: 1}))
	app.GET("/", func(c core.Ctx) error {
		if err := c.SendString("partial response"); err != nil {
			return err
		}
		return core.NewError(500, "failed later")
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	assertBody(t, rec, true, "partial response")
}

func TestGzipHeadersAndRequestIsolation(t *testing.T) {
	body := strings.Repeat("const number = 123;", 100)
	app := core.New()
	app.Use(compress.New())
	app.GET("/", func(c core.Ctx) error {
		c.SetHeader("Vary", "Origin")
		c.SetHeader("ETag", "\"asset-v1\"")
		c.SetHeader("Content-Type", "application/javascript")
		c.SetHeader("Content-Length", strconv.Itoa(len(body)))
		return c.SendString(body)
	})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/", nil)
			compressed := i%2 == 0
			if compressed {
				req.Header.Set("Accept-Encoding", "gzip")
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			assertBody(t, rec, compressed, body)
			if strings.Join(rec.Result().Header.Values("Vary"), ", ") != "Origin, Accept-Encoding" {
				t.Errorf("Vary=%v", rec.Result().Header.Values("Vary"))
			}
			if compressed {
				if rec.Result().Header.Get("ETag") != "W/\"asset-v1\"" {
					t.Errorf("ETag=%s", rec.Result().Header.Get("ETag"))
				}
			} else if rec.Result().Header.Get("Content-Length") != strconv.Itoa(len(body)) {
				t.Error("identity Content-Length changed")
			}
		}(i)
	}
	wg.Wait()
}

func TestCompressedStaticConditionalCache(t *testing.T) {
	app := core.New()
	app.Use(compress.New())
	app.StaticFS("/assets", http.FS(fstest.MapFS{
		"app.js": {Data: []byte(strings.Repeat("const value = 123;", 100)), ModTime: time.Unix(1700000000, 0)},
	}), core.StaticConfig{})
	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-Modified-Since", "Tue, 14 Nov 2023 22:13:20 GMT")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != 304 || rec.Result().Header.Get("Cache-Control") != "public, max-age=604800" {
		t.Fatalf("status=%d committed headers=%v", rec.Code, rec.Result().Header)
	}
	if rec.Body.Len() != 0 || rec.Result().Header.Get("Content-Encoding") != "" {
		t.Fatal("304 has a body or encoding")
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, cfg := range []compress.Config{
		{Level: 99},
		{ContentTypes: []string{"not a mime type"}},
	} {
		app := core.New()
		app.Use(compress.New(cfg))
		app.GET("/", func(c core.Ctx) error { return c.SendString("must not execute") })
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "must not execute") {
			t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
		}
	}
}

func TestFlushBelowThreshold(t *testing.T) {
	app := core.New()
	app.Use(compress.New())
	app.GET("/", func(c core.Ctx) error {
		if err := c.SendString("first"); err != nil {
			return err
		}
		c.Response().Flush()
		return c.SendString(strings.Repeat("second", 300))
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	assertBody(t, rec, false, "first"+strings.Repeat("second", 300))
	if !rec.Flushed {
		t.Fatal("flush was not forwarded")
	}
}

func TestEmptyWriteCommitsStatus(t *testing.T) {
	for _, accept := range []string{"", "gzip"} {
		app := core.New()
		app.Use(compress.New())
		app.GET("/", func(c core.Ctx) error {
			c.Status(http.StatusCreated)
			return c.SendString("")
		})
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", accept)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated || rec.Body.Len() != 0 || rec.Result().Header.Get("Content-Encoding") != "" {
			t.Fatalf("accept=%q status=%d headers=%v body=%q", accept, rec.Code, rec.Result().Header, rec.Body.String())
		}
	}
}

type failingWriter struct {
	header http.Header
	err    error
}

func (w *failingWriter) Header() http.Header       { return w.header }
func (w *failingWriter) WriteHeader(int)           {}
func (w *failingWriter) Write([]byte) (int, error) { return 0, w.err }
