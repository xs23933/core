package zstd_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
	"github.com/xs23933/core/v3"
	compress "github.com/xs23933/core/v3/middleware/zstd"
)

type copyRecorder struct {
	*httptest.ResponseRecorder
	copies int
}

func (w *copyRecorder) ReadFrom(r io.Reader) (int64, error) {
	w.copies++
	return io.Copy(w.ResponseRecorder, r)
}
func TestStaticTransferPreservesCompressionBoundary(t *testing.T) {
	body := strings.Repeat("const value = 12345;\n", 4096)
	for _, accept := range []string{"", "zstd", "zstd;q=0"} {
		t.Run(accept, func(t *testing.T) {
			app := core.New()
			app.Use(compress.New())
			app.StaticFS("/assets", http.FS(fstest.MapFS{"app.js": {Data: []byte(body)}}), core.StaticConfig{})
			dst := &copyRecorder{ResponseRecorder: httptest.NewRecorder()}
			req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
			req.Header.Set("Accept-Encoding", accept)
			app.ServeHTTP(dst, req)
			if dst.Code != 200 || dst.Header().Get("Cache-Control") != "public, max-age=604800" {
				t.Fatal(dst.Code, dst.Header())
			}
			if accept == "zstd" {
				if dst.copies != 0 || dst.Header().Get("Content-Encoding") != "zstd" {
					t.Fatal(dst.copies, dst.Header())
				}
				reader, err := zstd.NewReader(dst.Body)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				reader.Close()
				if string(data) != body {
					t.Fatal("compressed body changed")
				}
			} else if dst.copies != 1 || dst.Body.String() != body {
				t.Fatalf("copies=%d bytes=%d", dst.copies, dst.Body.Len())
			}
		})
	}
}

type failedSource struct{ err error }

func (r failedSource) Read([]byte) (int, error) { return 0, r.err }
func TestUncompressedCopyLeavesEmptyResponseUncommitted(t *testing.T) {
	for _, readErr := range []error{io.EOF, io.ErrUnexpectedEOF} {
		app := core.New()
		app.Use(compress.New())
		app.GET("/", func(c core.Ctx) error {
			n, err := io.Copy(c.Response(), failedSource{err: readErr})
			if n != 0 || c.Response().Written() {
				t.Fatalf("n=%d written=%t", n, c.Response().Written())
			}
			if readErr != io.EOF && err != readErr {
				t.Fatalf("err=%v", err)
			}
			return core.NewError(http.StatusBadGateway, "failure")
		})
		dst := httptest.NewRecorder()
		app.ServeHTTP(dst, httptest.NewRequest(http.MethodGet, "/", nil))
		if dst.Code != http.StatusBadGateway {
			t.Fatal(dst.Code, dst.Body.String())
		}
	}
}
