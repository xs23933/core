// Package etag provides conditional responses using representation validators.
package etag

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/xs23933/core/v3"
)

// Config ETag 配置
type Config struct {
	// Weak 是否为弱 ETag（前缀 W/）
	Weak bool
	// HeaderName 自定义 ETag 头字段，默认 ETag
	HeaderName string
	// Skip 可选跳过函数，返回 true 时跳过 ETag 处理
	Skip func(c core.Ctx) bool
}

var DefaultConfig = Config{
	Weak:       true,
	HeaderName: "ETag",
}

// New 返回 ETag 中间件。
func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		if conf[0].Weak {
			cfg.Weak = conf[0].Weak
		}
		if conf[0].HeaderName != "" {
			cfg.HeaderName = conf[0].HeaderName
		}
		if conf[0].Skip != nil {
			cfg.Skip = conf[0].Skip
		}
	}

	return func(c core.Ctx) error {
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}

		method := c.Method()
		if method != "GET" && method != "HEAD" {
			return c.Next()
		}

		original := c.Response()
		if original.Written() || c.GetHeader("Range") != "" || c.GetHeader("Upgrade") != "" {
			return c.Next()
		}
		w := &responseWriter{ResponseWriter: original, header: original.Header().Clone(), status: original.Status()}
		c.SetResponseWriter(w)
		defer c.SetResponseWriter(original)
		err := c.Next()
		if w.direct {
			return errors.Join(err, w.flushErr)
		}
		if err != nil {
			w.copyHeaders()
			if !w.written {
				return err
			}
			w.commit()
			if method == http.MethodHead || w.status == http.StatusNoContent || w.status == http.StatusNotModified {
				original.DoWriteHeader()
				return err
			}
			_, writeErr := original.Write(w.body.Bytes())
			return errors.Join(err, writeErr)
		}

		if w.status == http.StatusOK && !strings.Contains(strings.ToLower(w.header.Get("Cache-Control")), "no-store") {
			tag := w.header.Get(cfg.HeaderName)
			// HEAD may have no representation body; only trust an explicit validator.
			if tag == "" && method == http.MethodGet {
				hash := sha256.Sum256(w.body.Bytes())
				tag = "\"" + hex.EncodeToString(hash[:]) + "\""
				if cfg.Weak {
					tag = "W/" + tag
				}
				w.header.Set(cfg.HeaderName, tag)
			}
			if tag != "" && matchETag(tag, c.GetHeader("If-None-Match")) {
				w.status = http.StatusNotModified
				w.body.Reset()
				w.header.Del("Content-Length")
				w.header.Del("Content-Type")
				w.header.Del("Content-Encoding")
			}
		}
		w.commit()
		if method == http.MethodHead || w.status == http.StatusNotModified || w.status == http.StatusNoContent {
			original.DoWriteHeader()
			return nil
		}
		_, err = original.Write(w.body.Bytes())
		return err
	}
}

// Buffer small ordinary responses only. Flush, explicit commit, hijack and large
// bodies switch to passthrough, preserving streaming and static-file validators.
const maxBufferedBody = 1 << 20

type responseWriter struct {
	core.ResponseWriter
	header   http.Header
	body     bytes.Buffer
	status   int
	written  bool
	direct   bool
	flushErr error
}

func (w *responseWriter) Header() http.Header {
	if w.direct {
		return w.ResponseWriter.Header()
	}
	return w.header
}
func (w *responseWriter) Status() int {
	if w.direct {
		return w.ResponseWriter.Status()
	}
	return w.status
}
func (w *responseWriter) Size() int {
	if w.direct {
		return w.ResponseWriter.Size()
	}
	if !w.written {
		return -1
	}
	return w.body.Len()
}
func (w *responseWriter) Written() bool { return w.direct && w.ResponseWriter.Written() || w.written }
func (w *responseWriter) WriteHeader(code int) {
	if w.direct {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if !w.written && code > 0 {
		w.status = code
	}
}
func (w *responseWriter) copyHeaders() {
	clear(w.ResponseWriter.Header())
	for k, v := range w.header {
		w.ResponseWriter.Header()[k] = v
	}
}
func (w *responseWriter) commit() {
	w.copyHeaders()
	w.ResponseWriter.WriteHeader(w.status)
	w.direct = true
}
func (w *responseWriter) passthrough() error {
	if w.direct {
		return nil
	}
	w.commit()
	if w.body.Len() == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(w.body.Bytes())
	w.body.Reset()
	return err
}
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.direct {
		return w.ResponseWriter.Write(p)
	}
	if w.body.Len()+len(p) > maxBufferedBody || strings.HasPrefix(strings.ToLower(w.header.Get("Content-Type")), "text/event-stream") {
		if err := w.passthrough(); err != nil {
			return 0, err
		}
		return w.ResponseWriter.Write(p)
	}
	w.written = true
	return w.body.Write(p)
}
func (w *responseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *responseWriter) DoWriteHeader() {
	// This interface cannot return errors; report them when the middleware exits.
	if err := w.passthrough(); err != nil {
		w.flushErr = errors.Join(w.flushErr, err)
	}
	w.ResponseWriter.DoWriteHeader()
}
func (w *responseWriter) Flush() { w.DoWriteHeader(); w.ResponseWriter.Flush() }

func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	// Hijacking must not send an HTTP status before the caller's raw handshake.
	// Bytes already accepted before Hijack still belong to the HTTP response.
	if !w.direct && w.written {
		if err := w.passthrough(); err != nil {
			return nil, nil, err
		}
		w.ResponseWriter.Flush()
	}
	if !w.direct {
		for k, v := range w.header {
			w.ResponseWriter.Header()[k] = v
		}
		w.direct = true
	}
	return w.ResponseWriter.Hijack()
}

// matchETag 判断 client ETag 列表是否匹配 server ETag。
func matchETag(serverTag, clientHeader string) bool {
	tags := strings.Split(clientHeader, ",")
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == strings.TrimPrefix(serverTag, "W/") {
			return true
		}
	}
	return false
}
