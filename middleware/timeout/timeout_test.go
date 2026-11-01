package timeout

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
)

func TestCooperativeLifecycle(t *testing.T) {
	app := core.New()
	app.Use(New(Config{Timeout: time.Millisecond}))
	release := make(chan struct{})
	canceled := make(chan struct{})
	done := make(chan struct{})
	app.GET("/", func(c core.Ctx) error {
		for _, ctx := range []context.Context{c.Context(), c.Ctx(), c.Request().Context()} {
			if _, ok := ctx.Deadline(); !ok {
				return fmt.Errorf("no deadline")
			}
		}
		<-c.Context().Done()
		close(canceled)
		<-release
		if c.Request() == nil {
			return fmt.Errorf("released request")
		}
		calls := 0
		c.Stream(func(_ io.Writer) bool { calls++; return false })
		if calls != 0 {
			return fmt.Errorf("Stream missed cancellation")
		}
		return nil
	})
	rec := httptest.NewRecorder()
	go func() { app.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil)); close(done) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("handler did not observe cancellation")
	}
	select {
	case <-done:
		t.Fatal("returned while handler still active")
	default:
	}
	close(release)
	<-done
	if rec.Code != 503 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestBodyPreserved(t *testing.T) {
	for _, ct := range []string{"application/json", "application/xml", "multipart/form-data"} {
		t.Run(ct, func(t *testing.T) {
			app := core.New()
			app.Use(New(Config{Timeout: time.Second}))
			app.POST("/", func(c core.Ctx) error {
				if strings.HasPrefix(ct, "multipart/") {
					if c.FormValue("name") != "ok" {
						return fmt.Errorf("missing form")
					}
					file, err := c.FormFile("file")
					if err != nil {
						return err
					}
					reader, err := file.Open()
					if err != nil {
						return err
					}
					defer reader.Close()
					data, err := io.ReadAll(reader)
					if err != nil {
						return err
					}
					if string(data) != "upload" {
						return fmt.Errorf("missing file")
					}

				} else {
					var out struct {
						Name string `json:"name" xml:"name"`
					}
					if err := c.ReadBody(&out); err != nil {
						return err
					}
					if out.Name != "ok" {
						return fmt.Errorf("missing body")
					}
				}
				return c.SendString("ok")
			})
			var body bytes.Buffer
			contentType := ct
			switch ct {
			case "application/json":
				body.WriteString(`{"name":"ok"}`)
			case "application/xml":
				body.WriteString(`<root><name>ok</name></root>`)
			default:
				w := multipart.NewWriter(&body)
				if err := w.WriteField("name", "ok"); err != nil {
					t.Fatal(err)
				}
				file, err := w.CreateFormFile("file", "test.txt")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("upload")); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				contentType = w.FormDataContentType()
			}
			req := httptest.NewRequest("POST", "/", &body)
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != 200 || rec.Body.String() != "ok" {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestTimeoutResponseContract(t *testing.T) {
	for _, mode := range []string{"late-write", "committed", "parent", "skip", "error"} {
		t.Run(mode, func(t *testing.T) {
			app := core.New()
			app.Use(New(Config{Timeout: time.Millisecond, Skip: func(core.Ctx) bool { return mode == "skip" }}))
			app.GET("/", func(c core.Ctx) error {
				switch mode {
				case "skip":
					if _, ok := c.Context().Deadline(); ok {
						return fmt.Errorf("skip got deadline")
					}
					return c.SendString("skip")
				case "error":
					return core.NewError(400, "bad")
				case "committed":
					if err := c.SendString("early"); err != nil {
						return err
					}
				}
				<-c.Context().Done()
				return c.SendString("late")
			})
			req := httptest.NewRequest("GET", "/", nil)
			if mode == "parent" {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			want := 503
			switch mode {
			case "skip", "committed":
				want = 200
			case "error":
				want = 400
			}
			if rec.Code != want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if mode == "committed" && rec.Body.String() != "early" {
				t.Fatal(rec.Body)
			}
			if mode == "late-write" && rec.Body.String() != "request timeout" {
				t.Fatal(rec.Body)
			}
		})
	}
}

func TestTimeoutClearsUnsentRepresentationHeaders(t *testing.T) {
	app := core.New()
	app.Use(New(Config{Timeout: time.Millisecond}))
	app.GET("/", func(c core.Ctx) error {
		c.SetHeader("Content-Length", "100")
		c.SetHeader("Content-Encoding", "gzip")
		c.SetHeader("Content-Type", "application/json")
		c.SetHeader("ETag", `"success"`)
		c.SetHeader("Cache-Control", "public, max-age=3600")
		c.SetHeader("X-Request-ID", "kept")
		<-c.Context().Done()
		return c.Context().Err()
	})
	server := httptest.NewServer(app)
	defer server.Close()
	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 503 || string(data) != "request timeout" {
		t.Fatalf("status=%d body=%q error=%v", resp.StatusCode, data, err)
	}
	if resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("ETag") != "" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Request-ID") != "kept" {
		t.Fatal(resp.Header)
	}
}

func TestNestedDeadlineAndRecoveryRestore(t *testing.T) {
	app := core.New()
	app.Use(func(c core.Ctx) error {
		req, writer, ctx := c.Request(), c.Response(), c.Context()
		defer func() {
			if c.Request() != req || c.Response() != writer || c.Context() != ctx {
				t.Error("request context or writer not restored")
			}
			if c.GetStatus() != 500 {
				t.Error("recovery status lost")
			}
		}()
		return c.Next()
	}, New(Config{Timeout: time.Hour}), New(Config{Timeout: time.Second}))
	app.GET("/", func(c core.Ctx) error {
		deadline, ok := c.Context().Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Error("nested deadline lost")
		}
		panic("test")
	})
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
}
