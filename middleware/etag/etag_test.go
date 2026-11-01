package etag

import (
	"compress/gzip"
	"fmt"
	compress "github.com/xs23933/core/v3/middleware/gzip"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
)

func TestRepresentationValidation(t *testing.T) {
	app := core.New()
	app.Use(New())
	value := "old"
	calls := 0
	app.GET("/", func(c core.Ctx) error { calls++; return c.SendString(value) })
	get := func(tag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("If-None-Match", tag)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	first := get("")
	tag := first.Header().Get("ETag")
	if tag == "" || first.Body.String() != "old" {
		t.Fatal(first)
	}
	for _, match := range []string{tag, strings.TrimPrefix(tag, "W/"), `"other", ` + tag, "*"} {
		rec := get(match)
		if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "" {
			t.Fatalf("%s: %d %s", match, rec.Code, rec.Body)
		}
	}
	value = "new"
	rec := get(tag)
	if rec.Code != 200 || rec.Body.String() != "new" || rec.Header().Get("ETag") == tag {
		t.Fatal(rec)
	}
	if calls != 6 {
		t.Fatalf("handler calls=%d", calls)
	}
}
func TestResponseBoundaries(t *testing.T) {
	for _, mode := range []string{"head", "explicit", "no-store", "error", "flush", "large", "range", "post"} {
		t.Run(mode, func(t *testing.T) {
			app := core.New()
			app.Use(New())
			body := "hello"
			app.ALL("/", func(c core.Ctx) error {
				switch mode {
				case "explicit":
					c.SetHeader("ETag", `"version2"`)
				case "no-store":
					c.SetHeader("Cache-Control", "no-store")
				case "error":
					c.Status(404)
				case "flush":
					c.Response().Flush()
				case "large":
					body = strings.Repeat("x", 2<<20)
				}
				if c.Method() == "HEAD" {
					return nil
				}
				return c.SendString(body)
			})
			method := "GET"
			if mode == "head" {
				method = "HEAD"
			}
			if mode == "post" {
				method = "POST"
			}
			req := httptest.NewRequest(method, "/", nil)
			req.Header.Set("If-None-Match", `W/"version2"`)
			if mode == "range" {
				req.Header.Set("Range", "bytes=0-1")
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			switch mode {
			case "explicit":
				if rec.Code != 304 || rec.Body.Len() != 0 {
					t.Fatal(rec)
				}
			case "head":
				if rec.Header().Get("ETag") != "" || rec.Body.Len() != 0 {
					t.Fatal(rec)
				}
			default:
				if rec.Header().Get("ETag") != "" || rec.Body.String() != body {
					t.Fatal(rec.Code, rec.Header(), rec.Body.Len())
				}
			}
		})
	}
}
func TestStaticValidatorPreserved(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(file, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	app := core.New()
	app.Use(New())
	app.StaticFile("/file", file)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/file", nil))
	modified := rec.Header().Get("Last-Modified")
	if modified == "" {
		t.Fatal("missing Last-Modified")
	}
	req := httptest.NewRequest("GET", "/file", nil)
	req.Header.Set("If-Modified-Since", modified)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatal(rec)
	}
}

func TestHandlerErrorPreservesHeadersAndWrittenBody(t *testing.T) {
	for _, written := range []bool{false, true} {
		app := core.New()
		app.Use(New())
		app.GET("/", func(c core.Ctx) error {
			c.SetHeader("X-Test", "preserved")
			if written {
				if err := c.SendString("partial"); err != nil {
					return err
				}
			}
			return core.NewError(400, "bad")
		})
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Header().Get("X-Test") != "preserved" {
			t.Fatal(rec.Header())
		}
		if written {
			if rec.Code != 200 || rec.Body.String() != "partial" {
				t.Fatal(rec)
			}
		} else if rec.Code != 400 {
			t.Fatal(rec)
		}
	}
}

func TestGzipComposition(t *testing.T) {
	for _, outerETag := range []bool{false, true} {
		for _, size := range []int{2048, 2 << 20} {
			t.Run(fmt.Sprintf("etag-outer=%v/size=%d", outerETag, size), func(t *testing.T) {
				app := core.New()
				if outerETag {
					app.Use(New(), compress.New())
				} else {
					app.Use(compress.New(), New())
				}
				body := strings.Repeat("x", size)
				app.GET("/", func(c core.Ctx) error { c.SetHeader("Content-Type", "text/plain"); return c.SendString(body) })
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Accept-Encoding", "gzip")
				rec := httptest.NewRecorder()
				app.ServeHTTP(rec, req)
				if rec.Header().Get("Content-Encoding") != "gzip" {
					t.Fatal("compression lost")
				}
				zr, err := gzip.NewReader(rec.Body)
				if err != nil {
					t.Fatal(err)
				}
				defer zr.Close()
				got, err := io.ReadAll(zr)
				if err != nil || string(got) != body {
					t.Fatalf("body length=%d error=%v", len(got), err)
				}
			})
		}
	}
}

func TestHijackPreservesAcceptedBody(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(fmt.Sprint(written), func(t *testing.T) {
			app := core.New()
			app.Use(New())
			app.GET("/", func(c core.Ctx) error {
				if written {
					if err := c.SendString("accepted-prefix"); err != nil {
						return err
					}
				}
				conn, rw, err := c.Response().Hijack()
				if err != nil {
					return err
				}
				defer conn.Close()
				if !written {
					if _, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n"); err != nil {
						return err
					}
				}
				if _, err = rw.WriteString("raw-suffix"); err != nil {
					return err
				}
				return rw.Flush()
			})
			server := httptest.NewServer(app)
			defer server.Close()
			conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(conn)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "raw-suffix") {
				t.Fatalf("missing raw bytes: %q", data)
			}
			if written && !strings.Contains(string(data), "accepted-prefix") {
				t.Fatalf("accepted body lost: %q", data)
			}
			if !written && !strings.HasPrefix(string(data), "HTTP/1.1 101 ") {
				t.Fatalf("unexpected handshake: %q", data)
			}
		})
	}
}

func TestGzipConditionalOverHTTP(t *testing.T) {
	for _, outerETag := range []bool{false, true} {
		t.Run(fmt.Sprint(outerETag), func(t *testing.T) {
			app := core.New()
			if outerETag {
				app.Use(New(), compress.New())
			} else {
				app.Use(compress.New(), New())
			}
			app.GET("/", func(c core.Ctx) error {
				c.SetHeader("Content-Type", "text/plain")
				return c.SendString(strings.Repeat("text", 1024))
			})
			server := httptest.NewServer(app)
			defer server.Close()
			request := func(tag string) *http.Response {
				t.Helper()
				req, err := http.NewRequest("GET", server.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Accept-Encoding", "gzip")
				req.Header.Set("If-None-Match", tag)
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				return resp
			}
			first := request("")
			_, err := io.Copy(io.Discard, first.Body)
			first.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if first.Header.Get("Content-Encoding") != "gzip" || first.Header.Get("ETag") == "" {
				t.Fatal(first.Header)
			}
			second := request(first.Header.Get("ETag"))
			defer second.Body.Close()
			data, err := io.ReadAll(second.Body)
			if err != nil || second.StatusCode != 304 || len(data) != 0 {
				t.Fatalf("status=%d body=%q error=%v", second.StatusCode, data, err)
			}
		})
	}
}
