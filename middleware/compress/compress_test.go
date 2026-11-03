package compress

import (
	"compress/gzip"
	"errors"
	"fmt"
	brcodec "github.com/andybalholm/brotli"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	zstdcodec "github.com/klauspost/compress/zstd"
	"github.com/xs23933/core/v3"
	brmw "github.com/xs23933/core/v3/middleware/brotli"
	"github.com/xs23933/core/v3/middleware/etag"
	gzipmw "github.com/xs23933/core/v3/middleware/gzip"
	zstdmw "github.com/xs23933/core/v3/middleware/zstd"
)

func TestNegotiation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		values   []string
		coding   string
		identity bool
	}{
		{"missing", nil, "", true},
		{"empty", []string{""}, "", true},
		{"both", []string{"gzip, zstd"}, "zstd", true},
		{"br only", []string{"br"}, "br", true},
		{"all tie", []string{"gzip,br,zstd"}, "zstd", true},
		{"br before gzip", []string{"gzip,br"}, "br", true},
		{"br q lower", []string{"gzip;q=1,br;q=0.5"}, "gzip", true},
		{"br overrides star", []string{"*;q=1,zstd;q=0,br;q=0"}, "gzip", true},
		{"br duplicate deny", []string{"br;q=1,br;q=0,gzip"}, "gzip", true},
		{"gzip only", []string{"gzip"}, "gzip", true},
		{"zstd only", []string{"zstd"}, "zstd", true},
		{"q wins", []string{"zstd;q=0.2, gzip;q=0.8"}, "gzip", true},
		{"tie", []string{"gzip;q=0.5,zstd;q=0.5"}, "zstd", true},
		{"explicit zero", []string{"zstd;q=0, gzip"}, "gzip", true},
		{"wildcard", []string{"*"}, "zstd", true},
		{"explicit overrides star", []string{"*;q=1,zstd;q=0"}, "br", true},
		{"star denied explicit gzip", []string{"*;q=0,gzip;q=0.1"}, "gzip", false},
		{"all denied", []string{"*;q=0"}, "", false},
		{"identity exception", []string{"*;q=0,identity;q=0.2"}, "", true},
		{"identity forbidden", []string{"zstd,gzip,identity;q=0"}, "zstd", false},
		{"identity preferred", []string{"zstd;q=0.4,gzip;q=0.5,identity;q=0.9"}, "", true},
		{"identity tie", []string{"zstd,identity"}, "zstd", true},
		{"multiple fields", []string{"gzip;q=0.5", "ZSTD; Q=0.750"}, "zstd", true},
		{"duplicates deny", []string{"zstd;q=1", "zstd;q=0,gzip"}, "gzip", true},
		{"duplicates reversed", []string{"zstd;q=0,zstd;q=1,gzip"}, "gzip", true},
		{"duplicate star", []string{"*;q=1,*;q=0"}, "", false},
		{"unknown", []string{"deflate"}, "", true},
		{"malformed", []string{"zstd;q=NaN,gzip;q=1e0"}, "", true},
		{"too precise", []string{"zstd;q=0.1234,gzip"}, "gzip", true},
		{"extra params", []string{"zstd;q=1;q=0,gzip"}, "gzip", true},
		{"empty fractional", []string{"zstd;q=1.,gzip;q=0."}, "zstd", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			coding, identity := negotiate(tt.values)
			if coding != tt.coding || identity != tt.identity {
				t.Fatalf("%q/%t want %q/%t", coding, identity, tt.coding, tt.identity)
			}
		})
	}
}

func decode(t testing.TB, encoding string, data []byte) []byte {
	t.Helper()
	var reader io.ReadCloser
	switch encoding {
	case "gzip":
		var err error
		reader, err = gzip.NewReader(strings.NewReader(string(data)))
		if err != nil {
			t.Fatal(err)
		}
	case "br":
		out, err := io.ReadAll(brcodec.NewReader(strings.NewReader(string(data))))
		if err != nil {
			t.Fatal(err)
		}
		return out
	case "zstd":
		r, err := zstdcodec.NewReader(nil, zstdcodec.WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		out, err := r.DecodeAll(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	default:
		return data
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}
func newApp(mw core.HandlerFunc, handler core.HandlerFunc) *core.Core {
	app := core.New()
	app.Use(mw)
	app.GET("/", handler)
	return app
}
func request(app *core.Core, method, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

func TestResponseSelectionAndOriginalHeaders(t *testing.T) {
	body := strings.Repeat("const payload = 'hello world';\n", 200)
	for _, tt := range []struct {
		accept, encoding string
		status           int
	}{
		{"gzip,zstd", "zstd", 200}, {"gzip", "gzip", 200}, {"zstd;q=0.1,gzip;q=0.9", "gzip", 200},
		{"zstd;q=0,gzip", "gzip", 200}, {"*;q=0,gzip;q=0.1", "gzip", 200},
		{"zstd;q=0,zstd;q=1,gzip", "gzip", 200}, {"*;q=0,*;q=1,identity;q=1", "", 200},
		{"gzip;q=0.1,identity;q=0.9", "", 200}, {"", "", 200}, {"br", "br", 200},
		{"*;q=0", "", 406}, {"identity;q=0,deflate", "", 406},
	} {
		t.Run(tt.accept, func(t *testing.T) {
			called := 0
			app := newApp(New(), func(c core.Ctx) error {
				called++
				if c.GetHeader("Accept-Encoding") != tt.accept {
					t.Fatal("request header changed")
				}
				c.SetHeader("Vary", "Origin")
				c.SetHeader("Content-Type", "application/javascript")
				c.SetHeader("Content-Length", fmt.Sprint(len(body)))
				c.SetHeader("ETag", `"content"`)
				return c.SendString(body)
			})
			rec := request(app, "GET", tt.accept)
			if rec.Code != tt.status || rec.Header().Get("Content-Encoding") != tt.encoding {
				t.Fatal(rec.Code, rec.Header())
			}
			if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Accept-Encoding") {
				t.Fatal(rec.Header())
			}
			if tt.status == 406 {
				if called != 0 || rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(called, rec.Header())
				}
				return
			}
			if string(decode(t, tt.encoding, rec.Body.Bytes())) != body {
				t.Fatal("body changed")
			}
			if tt.encoding != "" && (rec.Header().Get("Content-Length") != "" || rec.Header().Get("ETag") != `W/"content"`) {
				t.Fatal(rec.Header())
			}
		})
	}
}

func TestEligibilityAndIdentityForbidden(t *testing.T) {
	for _, tt := range []struct {
		name, contentType, cache, encoding string
		small, flush                       bool
		cfg                                Config
	}{
		{name: "small", contentType: "text/plain", small: true},
		{name: "image", contentType: "image/png"},
		{name: "SSE", contentType: "text/event-stream"},
		{name: "no transform", contentType: "text/plain", cache: "no-transform"},
		{name: "existing coding", contentType: "text/plain", encoding: "br"},
		{name: "flush", contentType: "text/plain", small: true, flush: true},
		{name: "disabled types", contentType: "text/plain", cfg: Config{ContentTypes: []string{}}},
	} {
		for _, forbid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/identity-forbidden=%t", tt.name, forbid), func(t *testing.T) {
				body := strings.Repeat("payload", 500)
				if tt.small {
					body = "tiny"
				}
				app := newApp(New(tt.cfg), func(c core.Ctx) error {
					c.SetHeader("Content-Type", tt.contentType)
					c.SetHeader("Cache-Control", tt.cache)
					if tt.encoding != "" {
						c.SetHeader("Content-Encoding", tt.encoding)
					}
					if err := c.SendString(body); err != nil {
						return err
					}
					if tt.flush {
						c.Response().Flush()
					}
					return nil
				})
				accept := "gzip,zstd"
				if forbid {
					accept += ",identity;q=0"
				}
				rec := request(app, "GET", accept)
				if forbid && tt.encoding == "" {
					if rec.Code != 406 || rec.Body.String() != "Not Acceptable\n" || rec.Header().Get("Content-Encoding") != "" {
						t.Fatal(rec.Code, rec.Header(), rec.Body.String())
					}
				} else if rec.Code != 200 || rec.Body.String() != body || rec.Header().Get("Content-Encoding") != tt.encoding {
					t.Fatal(rec.Code, rec.Header())
				}
			})
		}
	}
}

func TestStaticAndETagComposition(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("static javascript;\n", 1000)
	file := filepath.Join(dir, "app.js")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, accept := range []string{"gzip,zstd", "gzip", "zstd;q=0.1,gzip;q=0.9"} {
		for _, outerETag := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/etag-outer=%t", accept, outerETag), func(t *testing.T) {
				app := core.New()
				if outerETag {
					app.Use(etag.New(), New())
				} else {
					app.Use(New(), etag.New())
				}
				app.StaticFile("/", file, core.StaticConfig{})
				rec := request(app, "GET", accept)
				encoding := "zstd"
				if accept != "gzip,zstd" {
					encoding = "gzip"
				}
				if rec.Code != 200 || rec.Header().Get("Content-Encoding") != encoding || string(decode(t, encoding, rec.Body.Bytes())) != body {
					t.Fatal(rec.Code, rec.Header())
				}
				if rec.Header().Get("Cache-Control") != "public, max-age=604800" {
					t.Fatal(rec.Header())
				}
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Accept-Encoding", accept)
				req.Header.Set("If-Modified-Since", rec.Header().Get("Last-Modified"))
				conditional := httptest.NewRecorder()
				app.ServeHTTP(conditional, req)
				if conditional.Code != 304 || conditional.Body.Len() != 0 {
					t.Fatal(conditional.Code, conditional.Body.String())
				}
				head := request(app, "HEAD", accept)
				if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Encoding") != "" {
					t.Fatal(head.Code, head.Header())
				}
				req = httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Accept-Encoding", accept)
				req.Header.Set("Range", "bytes=2-5")
				part := httptest.NewRecorder()
				app.ServeHTTP(part, req)
				if part.Code != 206 || part.Body.String() != body[2:6] || part.Header().Get("Content-Encoding") != "" {
					t.Fatal(part.Code, part.Header())
				}
			})
		}
	}
}

func TestConfigurationAndSkip(t *testing.T) {
	for _, cfg := range []Config{{BrotliLevel: 12}, {GzipLevel: 10}, {ZstdLevel: 10}, {ContentTypes: []string{"not a mime"}}} {
		app := newApp(New(cfg), func(c core.Ctx) error { t.Fatal("handler called for invalid config"); return nil })
		if rec := request(app, "GET", ""); rec.Code != 500 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	app := newApp(New(Config{Skip: func(core.Ctx) bool { return true }}), func(c core.Ctx) error { return c.SendString("skip") })
	rec := request(app, "GET", "*;q=0")
	if rec.Code != 200 || rec.Body.String() != "skip" || rec.Header().Get("Vary") != "" {
		t.Fatal(rec.Code, rec.Header())
	}
	app = newApp(New(Config{MinLength: -1, GzipLevel: 1, ZstdLevel: zstdcodec.SpeedFastest, ContentTypes: []string{"application/custom"}}), func(c core.Ctx) error { c.SetHeader("Content-Type", "application/custom"); return c.SendString("tiny") })
	rec = request(app, "GET", "zstd,gzip")
	if rec.Header().Get("Content-Encoding") != "zstd" || string(decode(t, "zstd", rec.Body.Bytes())) != "tiny" {
		t.Fatal(rec.Header())
	}
}

func TestErrorsEmptySourceAndConcurrentRequests(t *testing.T) {
	app := newApp(New(), func(c core.Ctx) error {
		if c.GetHeader("X-Error") == "before" {
			return core.NewError(502, "failure")
		}
		c.SetHeader("Content-Type", "text/plain")
		if c.GetHeader("X-Error") == "empty" {
			n, err := io.Copy(c.Response(), struct{ io.Reader }{strings.NewReader("")})
			if n != 0 || err != nil || c.Response().Written() {
				t.Fatalf("n=%d err=%v written=%t", n, err, c.Response().Written())
			}
			return core.NewError(502, "failure")
		}
		if err := c.SendString(strings.Repeat("payload", 1000)); err != nil {
			return err
		}
		if c.GetHeader("X-Error") == "after" {
			return errors.New("late error")
		}
		return nil
	})
	for _, accept := range []string{"gzip,zstd", "gzip", ""} {
		for _, stage := range []string{"before", "empty", "after"} {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", accept)
			req.Header.Set("X-Error", stage)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if stage == "after" {
				if rec.Code != 200 || string(decode(t, rec.Header().Get("Content-Encoding"), rec.Body.Bytes())) != strings.Repeat("payload", 1000) {
					t.Fatal(rec.Code, rec.Header())
				}
			} else if rec.Code != 502 {
				t.Fatal(rec.Code, rec.Body.String())
			}
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			accept := "gzip,zstd"
			if i%2 == 0 {
				accept = "gzip"
			}
			rec := request(app, "GET", accept)
			if rec.Code != 200 || string(decode(t, rec.Header().Get("Content-Encoding"), rec.Body.Bytes())) != strings.Repeat("payload", 1000) {
				t.Error("concurrent body mismatch")
			}
		}(i)
	}
}

func BenchmarkNegotiation(b *testing.B) {
	for _, value := range []string{"gzip, deflate, br, zstd", "zstd;q=0.5, gzip;q=1", ""} {
		b.Run(value, func(b *testing.B) {
			values := []string{value}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				negotiate(values)
			}
		})
	}
}
func BenchmarkCompression(b *testing.B) {
	var content strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&content, "const record%d = {id:%d, title:'resource %d', enabled:%t};\n", i, i, i, i%3 == 0)
	}
	body := content.String()
	payload := []byte(body)
	for _, tt := range []struct {
		name, accept string
		middleware   core.HandlerFunc
	}{
		{"gzip-direct", "gzip", gzipmw.New()},
		{"gzip-unified", "gzip", New()},
		{"br-direct", "br", brmw.New()},
		{"br-unified", "br,gzip", New()},
		{"zstd-direct", "zstd", zstdmw.New()},
		{"zstd-unified", "gzip,zstd", New()},
		{"identity-direct", "", func(c core.Ctx) error { return c.Next() }},
		{"identity-unified", "", New()},
	} {
		b.Run(tt.name, func(b *testing.B) {
			app := newApp(tt.middleware, func(c core.Ctx) error {
				c.SetHeader("Content-Type", "application/javascript")
				return c.Send(payload)
			})
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", tt.accept)
			// Warm the encoder and validate an actual complete representation.
			warm := httptest.NewRecorder()
			app.ServeHTTP(warm, req)
			if string(decode(b, warm.Header().Get("Content-Encoding"), warm.Body.Bytes())) != body {
				b.Fatal("body mismatch")
			}
			dst := &benchmarkWriter{header: make(http.Header)}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				clear(dst.header)
				dst.size = 0
				dst.status = 0
				app.ServeHTTP(dst, req)
				if dst.status != 200 || dst.size != warm.Body.Len() {
					b.Fatalf("status=%d size=%d", dst.status, dst.size)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(warm.Body.Len()), "wire-B/op")
		})
	}
}

func TestForbiddenIdentityBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name         string
		status       int
		body         string
		encoding     string
		handlerError bool
		want         int
	}{
		{name: "bodyless204", status: 204, want: 204},
		{name: "bodyless304", status: 304, want: 304},
		{name: "empty200", status: 200, want: 406},
		{name: "handler error", handlerError: true, want: 406},
		{name: "identity header", status: 200, encoding: "identity", body: "tiny", want: 406},
		{name: "plain redirect", status: 302, body: "redirect", want: 406},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := newApp(New(), func(c core.Ctx) error {
				if tt.handlerError {
					return core.NewError(502, "failure")
				}
				c.SetHeader("Content-Type", "text/plain")
				c.SetHeader("Content-Length", fmt.Sprint(len(tt.body)))
				c.SetHeader("Content-Disposition", "attachment; filename=private.txt")
				c.SetHeader("ETag", `"old"`)
				c.SetHeader("Last-Modified", "Mon, 02 Nov 2026 00:00:00 GMT")
				if tt.encoding != "" {
					c.SetHeader("Content-Encoding", tt.encoding)
				}
				c.Status(tt.status)
				if tt.body != "" {
					return c.SendString(tt.body)
				}
				c.Response().DoWriteHeader()
				return nil
			})
			rec := request(app, "GET", "zstd,gzip,identity;q=0")
			if rec.Code != tt.want {
				t.Fatal(rec.Code, rec.Header(), rec.Body.String())
			}
			if tt.want == 406 {
				if rec.Body.String() != "Not Acceptable\n" || rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("Content-Length") != "" {
					t.Fatal(rec.Header(), rec.Body.String())
				}
			} else if rec.Body.Len() != 0 {
				t.Fatal(rec.Body.String())
			}
		})
	}
	app := core.New()
	app.Use(New())
	app.HEAD("/", func(c core.Ctx) error { return c.SendStatus(502) })
	if rec := request(app, "HEAD", "zstd,identity;q=0"); rec.Code != 502 {
		t.Fatal(rec.Code)
	}
	if rec := request(app, "HEAD", "*;q=0"); rec.Code != 406 || rec.Body.Len() != 0 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

type countingRecorder struct {
	*httptest.ResponseRecorder
	copies int
}

func (w *countingRecorder) ReadFrom(r io.Reader) (int64, error) {
	w.copies++
	return io.Copy(w.ResponseRecorder, r)
}
func TestIdentityRetainsStaticFastPath(t *testing.T) {
	body := strings.Repeat("static asset;", 200)
	for _, accept := range []string{"", "*;q=0,identity;q=1"} {
		app := core.New()
		app.Use(New())
		app.StaticFS("/", http.FS(fstest.MapFS{"asset.js": {Data: []byte(body)}}), core.StaticConfig{})
		req := httptest.NewRequest("GET", "/asset.js", nil)
		req.Header.Set("Accept-Encoding", accept)
		dst := &countingRecorder{ResponseRecorder: httptest.NewRecorder()}
		app.ServeHTTP(dst, req)
		if dst.copies != 1 || dst.Body.String() != body || dst.Code != 200 {
			t.Fatal(dst.copies, dst.Code, dst.Body.Len())
		}
	}
}

func TestFlushAndWriteFailure(t *testing.T) {
	body := strings.Repeat("stream payload;", 200)
	for _, accept := range []string{"zstd,gzip", "gzip"} {
		app := newApp(New(), func(c core.Ctx) error {
			c.SetHeader("Content-Type", "text/plain")
			if err := c.SendString(body); err != nil {
				return err
			}
			c.Response().Flush()
			return c.SendString(body)
		})
		rec := request(app, "GET", accept)
		if !rec.Flushed || string(decode(t, rec.Header().Get("Content-Encoding"), rec.Body.Bytes())) != body+body {
			t.Fatal(rec.Header(), rec.Flushed)
		}
		sentinel := errors.New("writer failed")
		var observed error
		// Last separately registered middleware executes first.
		app.Use(func(c core.Ctx) error { observed = c.Next(); return observed })
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", accept)
		app.ServeHTTP(&failingWriter{header: make(http.Header), err: sentinel}, req)
		if !errors.Is(observed, sentinel) {
			t.Fatalf("write/flush/close error lost: %v", observed)
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
func (w *failingWriter) Flush()                    {}

// Discard the encoded bytes while counting the complete representation. Network
// and response-buffer costs are deliberately excluded from the codec benchmark.
type benchmarkWriter struct {
	header       http.Header
	size, status int
}

func (w *benchmarkWriter) Header() http.Header         { return w.header }
func (w *benchmarkWriter) WriteHeader(status int)      { w.status = status }
func (w *benchmarkWriter) Write(p []byte) (int, error) { w.size += len(p); return len(p), nil }

func TestDynamicConditionalETag(t *testing.T) {
	body := strings.Repeat("conditional representation;", 200)
	for _, accept := range []string{"gzip,zstd", "gzip"} {
		for _, outerETag := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/etag-outer=%t", accept, outerETag), func(t *testing.T) {
				app := core.New()
				if outerETag {
					app.Use(etag.New(), New())
				} else {
					app.Use(New(), etag.New())
				}
				app.GET("/", func(c core.Ctx) error { c.SetHeader("Content-Type", "text/plain"); return c.SendString(body) })
				first := request(app, "GET", accept)
				tag := first.Header().Get("ETag")
				if tag == "" || string(decode(t, first.Header().Get("Content-Encoding"), first.Body.Bytes())) != body {
					t.Fatal(first.Header())
				}
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("Accept-Encoding", accept)
				req.Header.Set("If-None-Match", tag)
				rec := httptest.NewRecorder()
				app.ServeHTTP(rec, req)
				if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("Content-Encoding") != "" {
					t.Fatal(rec.Code, rec.Header(), rec.Body.Len())
				}
			})
		}
	}
}

func TestPreEncodedResponseAtEntry(t *testing.T) {
	app := newApp(New(), func(c core.Ctx) error { return c.SendString("pre-encoded bytes") })
	app.Use(func(c core.Ctx) error { c.SetHeader("Content-Encoding", "br"); return c.Next() })
	rec := request(app, "GET", "deflate,identity;q=0")
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "br" || rec.Body.String() != "pre-encoded bytes" {
		t.Fatal(rec.Code, rec.Header(), rec.Body.String())
	}
}

type countingSource struct{ reads int }

func (r *countingSource) Read(p []byte) (int, error) {
	r.reads++
	if r.reads > 3 {
		return 0, io.EOF
	}
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
func TestRefusalStopsBodyProduction(t *testing.T) {
	for _, useCopy := range []bool{false, true} {
		t.Run(fmt.Sprintf("copy=%t", useCopy), func(t *testing.T) {
			source := &countingSource{}
			var writeErr, outerErr error
			app := newApp(New(), func(c core.Ctx) error {
				c.SetHeader("Content-Type", "text/event-stream")
				if useCopy {
					_, writeErr = io.Copy(c.Response(), source)
				} else {
					writeErr = c.SendString("event: private\n\n")
				}
				return writeErr
			})
			app.Use(func(c core.Ctx) error { outerErr = c.Next(); return outerErr })
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", "zstd,identity;q=0")
			// Range disables the codec up front, so ReadFrom can refuse before reading.
			if useCopy {
				req.Header.Set("Range", "bytes=0-10")
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != 406 || rec.Body.String() != "Not Acceptable\n" || !errors.Is(writeErr, errNotAcceptable) || outerErr != nil || source.reads != 0 {
				t.Fatalf("status=%d body=%q err=%v outer=%v reads=%d", rec.Code, rec.Body.String(), writeErr, outerErr, source.reads)
			}
		})
	}
	err := withoutRejection(fmt.Errorf("wrapped: %w", errors.Join(errNotAcceptable, io.ErrUnexpectedEOF)))
	if !errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errNotAcceptable) {
		t.Fatalf("unrelated error lost: %v", err)
	}
}

func TestRefusalStopsStreamingCopyAfterFirstRead(t *testing.T) {
	source := &countingSource{}
	app := newApp(New(), func(c core.Ctx) error {
		c.SetHeader("Content-Type", "text/event-stream")
		_, err := io.Copy(c.Response(), source)
		if !errors.Is(err, errNotAcceptable) {
			t.Fatalf("copy should stop: %v", err)
		}
		return err
	})
	rec := request(app, "GET", "zstd,identity;q=0")
	if rec.Code != 406 || source.reads != 1 || rec.Body.String() != "Not Acceptable\n" {
		t.Fatal(rec.Code, source.reads, rec.Body.String())
	}
}

func (*benchmarkWriter) Flush() {}

func BenchmarkSSECompression(b *testing.B) {
	var events []string
	var expected strings.Builder
	for i := 0; i < 32; i++ {
		event := fmt.Sprintf(`{"sequence":%d,"status":"active","payload":"streaming event content"}`, i)
		events = append(events, event)
		fmt.Fprintf(&expected, "event: update\ndata: %s\n\n", event)
	}
	for _, coding := range []string{"br", "gzip", "zstd"} {
		b.Run(coding, func(b *testing.B) {
			app := newApp(New(Config{EnableSSE: true}), func(c core.Ctx) error {
				c.SetHeader("Content-Type", "text/event-stream")
				c.Response().Flush()
				for _, event := range events {
					if err := c.SSEWrite("update", event); err != nil {
						return err
					}
				}
				return nil
			})
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", coding)
			warm := httptest.NewRecorder()
			app.ServeHTTP(warm, req)
			if string(decode(b, coding, warm.Body.Bytes())) != expected.String() {
				b.Fatal("decoded SSE differs")
			}
			dst := &benchmarkWriter{header: make(http.Header)}
			b.SetBytes(int64(expected.Len()))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				clear(dst.header)
				dst.size = 0
				dst.status = 0
				app.ServeHTTP(dst, req)
				if dst.status != 200 || dst.size != warm.Body.Len() {
					b.Fatal(dst.status, dst.size)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(warm.Body.Len()), "body-B/stream")
			b.ReportMetric(32, "events/stream")
		})
	}
}
