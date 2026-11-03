package compress

import (
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	brcodec "github.com/andybalholm/brotli"
	zstdcodec "github.com/klauspost/compress/zstd"
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/middleware/brotli"
	"github.com/xs23933/core/v3/middleware/etag"
	gzipmw "github.com/xs23933/core/v3/middleware/gzip"
	zstdmw "github.com/xs23933/core/v3/middleware/zstd"
)

func streamDecoder(t *testing.T, coding string, source io.Reader) io.Reader {
	t.Helper()
	switch coding {
	case "gzip":
		r, err := gzip.NewReader(source)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := r.Close(); err != nil {
				t.Error(err)
			}
		})
		return r
	case "br":
		return brcodec.NewReader(source)
	case "zstd":
		r, err := zstdcodec.NewReader(source, zstdcodec.WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Close)
		return r
	default:
		t.Fatalf("unexpected codec %q", coding)
		return nil
	}
}

// A decoder must receive each tiny event while the handler and encoder remain
// open, including when the SSE headers were flushed before the first event.
func TestSSEEventsArriveBeforeClose(t *testing.T) {
	for _, tt := range []struct {
		name, accept, coding string
		mw                   core.HandlerFunc
	}{
		{"gzip", "gzip", "gzip", gzipmw.New(gzipmw.Config{EnableSSE: true})},
		{"br", "br", "br", brotli.New(brotli.Config{EnableSSE: true})},
		{"zstd", "zstd", "zstd", zstdmw.New(zstdmw.Config{EnableSSE: true})},
		{"unified-br", "br,gzip", "br", New(Config{EnableSSE: true})},
		{"unified-gzip-q", "br;q=0.2,gzip;q=1", "gzip", New(Config{EnableSSE: true})},
		{"unified-zstd", "br,gzip,zstd", "zstd", New(Config{EnableSSE: true})},
	} {
		for _, outerETag := range []bool{false, true} {
			for _, initialMode := range []string{"write", "flush", "commit"} {
				t.Run(tt.name+"/"+initialMode+map[bool]string{false: "/etag-inner", true: "/etag-outer"}[outerETag], func(t *testing.T) {
					app := core.New()
					if outerETag {
						app.Use(etag.New(), tt.mw)
					} else {
						app.Use(tt.mw, etag.New())
					}
					ack := make(chan struct{})
					release := make(chan struct{})
					app.GET("/events", func(c core.Ctx) error {
						c.SetHeader("Content-Type", "text/event-stream; charset=utf-8")
						c.SetHeader("Cache-Control", "no-cache")
						if initialMode == "commit" {
							c.Response().DoWriteHeader()
						} else if initialMode == "flush" {
							c.Response().Flush()
						}
						for _, data := range []string{"first", "second"} {
							if err := c.SSEWrite("message", data); err != nil {
								return err
							}
							select {
							case <-ack:
							case <-c.Context().Done():
								return c.Context().Err()
							}
						}
						select {
						case <-release:
						case <-c.Context().Done():
						}
						return nil
					})
					server := httptest.NewServer(app)
					defer server.Close()
					defer close(release)
					ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
					defer cancel()
					req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/events", nil)
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Accept-Encoding", tt.accept)
					response, err := server.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					if response.Header.Get("Content-Encoding") != tt.coding || response.Header.Get("Content-Length") != "" {
						t.Fatal(response.Header)
					}
					reader := bufio.NewReader(streamDecoder(t, tt.coding, response.Body))
					for _, data := range []string{"first", "second"} {
						for _, want := range []string{"event: message\n", "data: " + data + "\n", "\n"} {
							got, err := reader.ReadString('\n')
							if err != nil || got != want {
								t.Fatalf("got=%q want=%q err=%v", got, want, err)
							}
						}
						select {
						case ack <- struct{}{}:
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
				})
			}
		}
	}
}

func TestSSEOptInRestrictions(t *testing.T) {
	for _, coding := range []string{"br", "gzip", "zstd"} {
		for _, tt := range []struct {
			name        string
			cfg         Config
			noTransform bool
		}{
			{name: "default-off"},
			{name: "no-transform", cfg: Config{EnableSSE: true}, noTransform: true},
			{name: "empty-types", cfg: Config{EnableSSE: true, ContentTypes: []string{}}},
		} {
			t.Run(coding+"/"+tt.name, func(t *testing.T) {
				app := newApp(New(tt.cfg), func(c core.Ctx) error {
					c.SetHeader("Content-Type", "text/event-stream")
					if tt.noTransform {
						c.SetHeader("Cache-Control", "no-transform")
					}
					c.Response().Flush()
					return c.SSEComment("heartbeat")
				})
				rec := request(app, "GET", coding)
				if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Body.String(), "heartbeat") {
					t.Fatal(rec.Code, rec.Header(), rec.Body.String())
				}
			})
		}
	}
}
