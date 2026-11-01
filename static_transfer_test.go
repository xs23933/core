package core

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type transferReader struct{ io.Reader }
type transferWriter struct {
	header http.Header
	size   int
	calls  int
	err    error
}

func (w *transferWriter) Header() http.Header         { return w.header }
func (w *transferWriter) WriteHeader(int)             {}
func (w *transferWriter) Write(p []byte) (int, error) { w.size += len(p); return len(p), nil }
func (w *transferWriter) ReadFrom(r io.Reader) (int64, error) {
	w.calls++
	n, err := io.Copy(io.Discard, r)
	w.size += int(n)
	if w.err != nil {
		return n, w.err
	}
	return n, err
}
func TestResponseTransferFastPath(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "cached"}[cached], func(t *testing.T) {
			sentinel := errors.New("transfer failed")
			dst := &transferWriter{header: make(http.Header), err: sentinel}
			base := &resp{}
			base.init(dst)
			var writer io.Writer = base
			if cached {
				writer = &staticCacheWriter{ResponseWriter: base, policy: "public, max-age=60", filename: "app.js"}
			}
			n, err := io.Copy(writer, transferReader{strings.NewReader("payload")})
			if n != 7 || !errors.Is(err, sentinel) || dst.calls != 1 || base.Size() != 7 || !base.Written() {
				t.Fatalf("n=%d err=%v calls=%d size=%d", n, err, dst.calls, base.Size())
			}
			if cached && dst.Header().Get("Cache-Control") != "public, max-age=60" {
				t.Fatal(dst.Header())
			}
		})
	}
}
func TestResponseTransferFallback(t *testing.T) {
	dst := httptest.NewRecorder()
	base := &resp{}
	base.init(dst)
	n, err := io.Copy(base, transferReader{strings.NewReader("payload")})
	if err != nil || n != 7 || base.Size() != 7 || dst.Body.String() != "payload" {
		t.Fatalf("n=%d err=%v size=%d body=%q", n, err, base.Size(), dst.Body.String())
	}
}
func BenchmarkStaticTransfer(b *testing.B) {
	body := strings.Repeat("x", 1<<20)
	for _, cached := range []bool{false, true} {
		b.Run(map[bool]string{false: "plain", true: "cached"}[cached], func(b *testing.B) {
			dst := &transferWriter{header: make(http.Header)}
			base := &resp{}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				base.init(dst)
				var writer io.Writer = base
				if cached {
					writer = &staticCacheWriter{ResponseWriter: base, policy: "public, max-age=60", filename: "app.js"}
				}
				if _, err := io.Copy(writer, transferReader{strings.NewReader(body)}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkStaticFileTransfer reads the complete response over loopback TCP.
// legacy hides the transport fast path to reproduce the old copy behavior.
func BenchmarkStaticFileTransfer(b *testing.B) {
	dir := b.TempDir()
	filename := dir + "/asset.bin"
	if err := os.WriteFile(filename, []byte(strings.Repeat("x", 1<<20)), 0600); err != nil {
		b.Fatal(err)
	}
	for _, cached := range []bool{false, true} {
		app := New()
		if cached {
			app.StaticFile("/asset.bin", filename, StaticConfig{})
		} else {
			app.StaticFile("/asset.bin", filename)
		}
		for _, legacy := range []bool{true, false} {
			b.Run(fmt.Sprintf("cached=%t/legacy=%t", cached, legacy), func(b *testing.B) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if legacy {
						w = struct{ http.ResponseWriter }{w}
					}
					app.ServeHTTP(w, r)
				}))
				defer server.Close()
				client := server.Client()
				defer client.CloseIdleConnections()
				b.SetBytes(1 << 20)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					response, err := client.Get(server.URL + "/asset.bin")
					if err != nil {
						b.Fatal(err)
					}
					n, err := io.Copy(io.Discard, response.Body)
					closeErr := response.Body.Close()
					if err != nil || closeErr != nil || n != 1<<20 || response.StatusCode != 200 {
						b.Fatalf("n=%d err=%v close=%v status=%d", n, err, closeErr, response.StatusCode)
					}
				}
			})
		}
	}
}

func TestStaticFileTransferCompatibility(t *testing.T) {
	dir := t.TempDir()
	filename := dir + "/asset.js"
	if err := os.WriteFile(filename, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, api := range []string{"Static", "StaticFS", "StaticFile", "StaticFileFS"} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%t", api, cached), func(t *testing.T) {
				app := New()
				var cfg []StaticConfig
				if cached {
					cfg = []StaticConfig{{}}
				}
				switch api {
				case "Static":
					app.Static("/assets", dir, cfg...)
				case "StaticFS":
					app.StaticFS("/assets", http.Dir(dir), cfg...)
				case "StaticFile":
					app.StaticFile("/assets/asset.js", filename, cfg...)
				case "StaticFileFS":
					app.StaticFileFS("/assets/asset.js", "asset.js", http.Dir(dir), cfg...)
				}
				dst := &transferWriter{header: make(http.Header)}
				req := httptest.NewRequest(http.MethodGet, "/assets/asset.js", nil)
				app.ServeHTTP(dst, req)
				if dst.calls != 1 || dst.size != 10 {
					t.Fatalf("calls=%d size=%d", dst.calls, dst.size)
				}
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					req := httptest.NewRequest(method, "/assets/asset.js", nil)
					req.Header.Set("Range", "bytes=2-5")
					rec := httptest.NewRecorder()
					app.ServeHTTP(rec, req)
					want := "2345"
					if method == http.MethodHead {
						want = ""
					}
					if rec.Code != 206 || rec.Body.String() != want || rec.Header().Get("Content-Range") != "bytes 2-5/10" {
						t.Fatalf("status=%d body=%q headers=%v", rec.Code, rec.Body.String(), rec.Header())
					}
				}
			})
		}
	}
}

type transformingResponse struct{ ResponseWriter }

func (w *transformingResponse) Write(p []byte) (int, error) {
	return w.ResponseWriter.Write([]byte(strings.ToUpper(string(p))))
}
func TestStaticTransferRetainsMiddlewareWrites(t *testing.T) {
	dst := httptest.NewRecorder()
	base := &resp{}
	base.init(dst)
	writer := &staticCacheWriter{ResponseWriter: &transformingResponse{ResponseWriter: base}, policy: "public, max-age=60", filename: "app.js"}
	n, err := io.Copy(writer, transferReader{strings.NewReader("payload")})
	if n != 7 || err != nil || dst.Body.String() != "PAYLOAD" || base.Size() != 7 {
		t.Fatalf("n=%d err=%v body=%q size=%d", n, err, dst.Body.String(), base.Size())
	}
}

type failingTransferReader struct{ err error }

func (r failingTransferReader) Read([]byte) (int, error) { return 0, r.err }
func TestResponseTransferDoesNotCommitEmptySource(t *testing.T) {
	for _, readErr := range []error{io.EOF, errors.New("read failure")} {
		dst := httptest.NewRecorder()
		base := &resp{}
		base.init(dst)
		n, err := io.Copy(base, failingTransferReader{err: readErr})
		if n != 0 || base.Written() || (readErr != io.EOF && !errors.Is(err, readErr)) || (readErr == io.EOF && err != nil) {
			t.Fatalf("n=%d err=%v written=%t", n, err, base.Written())
		}
		base.WriteHeader(http.StatusBadGateway)
		if _, err := base.WriteString("failure"); err != nil {
			t.Fatal(err)
		}
		if dst.Code != http.StatusBadGateway {
			t.Fatal(dst.Code)
		}
	}
}
