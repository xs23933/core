package brotli_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	codec "github.com/andybalholm/brotli"
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/middleware/brotli"
)

func TestNegotiationAndResponseHeaders(t *testing.T) {
	body := strings.Repeat("compressible body;", 1000)
	for _, tt := range []struct {
		accept, typ, cache, encoding string
		compressed                   bool
	}{
		{"br", "text/plain", "", "", true},
		{"br;q=0.5", "application/json", "", "", true},
		{"*", "text/css", "", "", true},
		{"br;q=0,*;q=1", "text/plain", "", "", false},
		{"gzip", "text/plain", "", "", false},
		{"", "text/plain", "", "", false},
		{"br", "image/png", "", "", false},
		{"br", "text/plain", "no-transform", "", false},
		{"br", "text/plain", "", "gzip", false},
		{"br", "text/event-stream", "", "", false},
	} {
		t.Run(tt.accept+tt.typ+tt.cache+tt.encoding, func(t *testing.T) {
			app := core.New()
			app.Use(brotli.New())
			app.GET("/", func(c core.Ctx) error {
				c.SetHeader("Content-Type", tt.typ)
				c.SetHeader("Cache-Control", tt.cache)
				c.SetHeader("Content-Encoding", tt.encoding)
				c.SetHeader("Content-Length", strconv.Itoa(len(body)))
				c.SetHeader("ETag", `"tag"`)
				return c.SendString(body)
			})
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", tt.accept)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatal(rec.Code)
			}
			if tt.compressed {
				got, err := io.ReadAll(codec.NewReader(bytes.NewReader(rec.Body.Bytes())))
				if err != nil || string(got) != body {
					t.Fatal(err, len(got))
				}
				if rec.Header().Get("Content-Encoding") != "br" || rec.Header().Get("Content-Length") != "" || rec.Header().Get("ETag") != `W/"tag"` {
					t.Fatal(rec.Header())
				}
			} else if rec.Header().Get("Content-Encoding") != tt.encoding || rec.Body.String() != body {
				t.Fatal(rec.Header(), rec.Body.Len())
			}
		})
	}
}

func TestStaticRangesAndConditional(t *testing.T) {
	body := strings.Repeat("static asset;", 200)
	app := core.New()
	app.Use(brotli.New())
	app.StaticFS("/", http.FS(fstest.MapFS{"app.js": {Data: []byte(body), ModTime: time.Unix(1700000000, 0)}}), core.StaticConfig{})
	for _, tt := range []struct {
		method, rangeHeader, modified string
		status                        int
	}{
		{"GET", "", "", 200}, {"HEAD", "", "", 200}, {"GET", "bytes=2-5", "", 206},
		{"GET", "", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat), 304},
	} {
		req := httptest.NewRequest(tt.method, "/app.js", nil)
		req.Header.Set("Accept-Encoding", "br")
		req.Header.Set("Range", tt.rangeHeader)
		req.Header.Set("If-Modified-Since", tt.modified)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != tt.status || rec.Header().Get("Cache-Control") != "public, max-age=604800" {
			t.Fatal(rec.Code, rec.Header())
		}
		if tt.status == 206 {
			if rec.Body.String() != body[2:6] {
				t.Fatal(rec.Body.String())
			}
		} else if tt.method == "HEAD" || tt.status == 304 {
			if rec.Body.Len() != 0 {
				t.Fatal(rec.Body.Len())
			}
		}
	}
}

func TestInvalidConfigAndErrors(t *testing.T) {
	for _, cfg := range []brotli.Config{{Level: -1}, {Level: 12}, {ContentTypes: []string{"invalid mime"}}} {
		app := core.New()
		app.Use(brotli.New(cfg))
		app.GET("/", func(c core.Ctx) error { t.Fatal("invalid config called handler"); return nil })
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != 500 {
			t.Fatal(rec.Code)
		}
	}
	sentinel := errors.New("write failed")
	var observed error
	app := core.New()
	app.Use(brotli.New())
	app.Use(func(c core.Ctx) error { observed = c.Next(); return observed })
	app.GET("/", func(c core.Ctx) error {
		c.SetHeader("Content-Type", "text/plain")
		return c.SendString(strings.Repeat("payload", 1000))
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "br")
	app.ServeHTTP(&failedWriter{header: make(http.Header), err: sentinel}, req)
	if !errors.Is(observed, sentinel) {
		t.Fatalf("close error lost: %v", observed)
	}
}

type failedWriter struct {
	header http.Header
	err    error
}

func (w *failedWriter) Header() http.Header       { return w.header }
func (w *failedWriter) WriteHeader(int)           {}
func (w *failedWriter) Write([]byte) (int, error) { return 0, w.err }

func TestAcceptedBrotliBodySurvivesLateHandlerError(t *testing.T) {
	body := strings.Repeat("buffered brotli body;", 1000)
	app := core.New()
	app.Use(brotli.New())
	app.GET("/", func(c core.Ctx) error {
		c.SetHeader("Content-Type", "text/plain")
		if err := c.SendString(body); err != nil {
			return err
		}
		return errors.New("late handler failure")
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "br")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	got, err := io.ReadAll(codec.NewReader(bytes.NewReader(rec.Body.Bytes())))
	if err != nil || rec.Code != 200 || string(got) != body {
		t.Fatal(rec.Code, err, len(got))
	}
}
