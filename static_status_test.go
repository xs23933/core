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

// TestStaticDefaultStatuses 验证未配置缓存时也正确提交无正文状态。
func TestStaticDefaultStatuses(t *testing.T) {
	files := http.FS(fstest.MapFS{"index.html": {Data: []byte("home")}, "dir/index.html": {Data: []byte("page")}, "app.js": {Data: []byte("0123456789"), ModTime: time.Unix(1700000000, 0)}})
	for _, api := range []string{"StaticFS", "Static", "StaticFile", "StaticFileFS"} {
		t.Run(api, func(t *testing.T) {
			app := New(Options{})
			switch api {
			case "StaticFS":
				app.StaticFS("/", files)
			case "Static":
				app.Static("/", writeStaticStatusFixture(t))
			case "StaticFile":
				app.StaticFile("/app.js", writeStaticStatusFixture(t)+"/app.js")
			case "StaticFileFS":
				app.StaticFileFS("/app.js", "app.js", files)
			}
			for _, tt := range []struct {
				method, path, header, value string
				status                      int
			}{
				{"HEAD", "/app.js", "", "", 200},
				{"GET", "/app.js", "If-Modified-Since", "Tue, 14 Nov 2023 22:13:20 GMT", 304},
			} {
				req := httptest.NewRequest(tt.method, tt.path, nil)
				if tt.header != "" {
					req.Header.Set(tt.header, tt.value)
				}
				rec := httptest.NewRecorder()
				app.ServeHTTP(rec, req)
				if rec.Code != tt.status || rec.Body.Len() != 0 {
					t.Fatalf("%s: status=%d body=%q", tt.method, rec.Code, rec.Body.String())
				}
				if rec.Header().Get("Cache-Control") != "" {
					t.Fatal("未配置缓存却添加了缓存策略")
				}
			}
			if api == "StaticFS" || api == "Static" {
				rec := httptest.NewRecorder()
				app.ServeHTTP(rec, httptest.NewRequest("GET", "/dir", nil))
				if rec.Code != 301 {
					t.Fatalf("目录跳转状态=%d", rec.Code)
				}
			}
		})
	}
}

// writeStaticStatusFixture 创建带固定修改时间的文件，避免依赖测试时钟。
func writeStaticStatusFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.js", "dir/index.html"} {
		filename := filepath.Join(root, name)
		if err := os.WriteFile(filename, []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(1700000000, 0)
		if err := os.Chtimes(filename, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
