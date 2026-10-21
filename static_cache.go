package core

import (
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// StaticConfig opts a static mount into browser/CDN caching. Omit the config to
// retain the original behavior. Existing Cache-Control headers take precedence.
type StaticConfig struct {
	// MaxAge defaults to 7 days when zero; a negative value requests revalidation.
	MaxAge time.Duration
	// Immutable is only appropriate when changing content also changes its URL.
	// HTML always uses no-cache so entry documents can discover new assets.
	Immutable bool
}

func withStaticCache(handler HandlerFunc, filename string, config []StaticConfig) HandlerFunc {
	if len(config) == 0 {
		return handler
	}
	cfg := config[0]
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 7 * 24 * time.Hour
	}
	policy := "no-cache"
	if seconds := int64(cfg.MaxAge / time.Second); seconds > 0 {
		policy = "public, max-age=" + strconv.FormatInt(seconds, 10)
		if cfg.Immutable {
			policy += ", immutable"
		}
	}
	return func(c Ctx) error {
		original := c.Response()
		name := filename
		if name == "" {
			name = c.Path()
		}
		writer := &staticCacheWriter{ResponseWriter: original, policy: policy, filename: name}
		c.SetResponseWriter(writer)
		defer c.SetResponseWriter(original)
		return handler(c)
	}
}

type staticCacheWriter struct {
	ResponseWriter
	policy, filename string
}

func (w *staticCacheWriter) applyPolicy(status int) {
	if w.Written() || w.Header().Get("Cache-Control") != "" {
		return
	}
	switch status {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
		policy := w.policy
		contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(w.Header().Get("Content-Type"), ";", 2)[0]))
		ext := strings.ToLower(path.Ext(w.filename))
		if contentType == "text/html" || contentType == "application/xhtml+xml" || ext == ".html" || ext == ".htm" || strings.HasSuffix(w.filename, "/") {
			policy = "no-cache"
		}
		w.Header().Set("Cache-Control", policy)
	}
}

func (w *staticCacheWriter) WriteHeader(code int) {
	// Apply before delegating: another wrapper may commit bodyless responses
	// immediately from WriteHeader (e.g. gzip when receiving a 304).
	w.applyPolicy(code)
	w.ResponseWriter.WriteHeader(code)
	// Core defers committing headers until the body is written. Preserve that
	// behavior for compression, but commit bodyless/redirect/error responses.
	if code >= 300 || code == http.StatusNoContent {
		w.DoWriteHeader()
	}
}

func (w *staticCacheWriter) DoWriteHeader() {
	w.applyPolicy(w.Status())
	w.ResponseWriter.DoWriteHeader()
}

func (w *staticCacheWriter) Write(p []byte) (int, error) {
	w.applyPolicy(w.Status())
	return w.ResponseWriter.Write(p)
}

func (w *staticCacheWriter) WriteString(s string) (int, error) {
	w.applyPolicy(w.Status())
	return w.ResponseWriter.WriteString(s)
}

func (w *staticCacheWriter) Flush() {
	w.DoWriteHeader()
	w.ResponseWriter.Flush()
}
