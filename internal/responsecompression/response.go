// Package responsecompression shares streaming response behavior between codecs.
package responsecompression

import (
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/xs23933/core/v3"
)

// Writer is the common lifecycle implemented by gzip, Brotli and zstd encoders.
type Writer interface {
	io.WriteCloser
	Flush() error
	Reset(io.Writer)
}

type Config struct {
	Encoding     string
	ContentTypes []string
	MinLength    int
	EnableSSE    bool
	Skip         func(core.Ctx) bool
	NewWriter    func(io.Writer) (Writer, error)
}

func New(cfg Config) core.HandlerFunc {
	if cfg.MinLength < 0 {
		cfg.MinLength = 0
	}
	types := make(map[string]bool, len(cfg.ContentTypes))
	for _, value := range cfg.ContentTypes {
		mediaType, _, err := mime.ParseMediaType(value)
		if err != nil {
			return configError(fmt.Errorf("%s content type %q: %w", cfg.Encoding, value, err))
		}
		types[strings.ToLower(mediaType)] = true
	}
	initial, err := cfg.NewWriter(io.Discard)
	if err != nil {
		return configError(fmt.Errorf("%s level: %w", cfg.Encoding, err))
	}
	pool := &sync.Pool{}
	pool.Put(initial)
	return func(c core.Ctx) error {
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}
		original := c.Response()
		if original.Written() {
			return c.Next()
		}
		addVary(original.Header())
		enabled := c.Method() != http.MethodHead && c.Method() != http.MethodConnect && c.GetHeader("Upgrade") == "" && c.GetHeader("Range") == "" && Accepts(c.Request().Header.Values("Accept-Encoding"), cfg.Encoding)
		w := &responseWriter{ResponseWriter: original, types: types, cfg: cfg, pool: pool, enabled: enabled}
		c.SetResponseWriter(w)
		defer c.SetResponseWriter(original)
		defer w.release()
		err := c.Next()
		if err != nil && !w.Written() {
			return err
		}
		return errors.Join(err, w.finish())
	}
}

func configError(err error) core.HandlerFunc {
	return func(core.Ctx) error { return err }
}

func addVary(h http.Header) {
	for _, value := range h.Values("Vary") {
		for _, field := range strings.Split(value, ",") {
			field = strings.TrimSpace(field)
			if field == "*" || strings.EqualFold(field, "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}

func Accepts(values []string, coding string) bool {
	wildcard := false
	for _, item := range strings.Split(strings.Join(values, ","), ",") {
		parts := strings.Split(item, ";")
		encoding := strings.TrimSpace(parts[0])
		if !strings.EqualFold(encoding, coding) && encoding != "*" {
			continue
		}
		quality := 1.0
		for _, param := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || !strings.EqualFold(key, "q") {
				quality = 0
				break
			}
			q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || math.IsNaN(q) || q < 0 || q > 1 {
				quality = 0
				break
			}
			quality = q
		}
		if strings.EqualFold(encoding, coding) {
			return quality > 0
		}
		wildcard = quality > 0
	}
	return wildcard
}

type responseWriter struct {
	core.ResponseWriter
	types    map[string]bool
	cfg      Config
	pool     *sync.Pool
	writer   Writer
	pending  []byte
	accepted int
	decided  bool
	enabled  bool
	err      error
}

func (w *responseWriter) Written() bool {
	return len(w.pending) > 0 || w.accepted > 0 || w.ResponseWriter.Written()
}

func (w *responseWriter) Size() int {
	if !w.decided && len(w.pending) > 0 {
		return len(w.pending)
	}
	return w.ResponseWriter.Size()
}

func (w *responseWriter) WriteHeader(code int) {
	if w.Written() {
		return
	}
	w.ResponseWriter.WriteHeader(code)
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified {
		w.DoWriteHeader()
	}
}

func (w *responseWriter) sse() bool {
	if !w.cfg.EnableSSE {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
	return w.cfg.EnableSSE && err == nil && strings.EqualFold(mediaType, "text/event-stream")
}

func (w *responseWriter) eligible(sample []byte) bool {
	if !w.enabled {
		return false
	}
	h := w.Header()
	status := w.Status()
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified || status == http.StatusPartialContent || h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	for _, line := range h.Values("Cache-Control") {
		for _, directive := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(directive), "no-transform") {
				return false
			}
		}
	}
	contentType := h.Get("Content-Type")
	if contentType == "" {
		if len(sample) == 0 {
			return false
		}
		contentType = http.DetectContentType(sample)
		h.Set("Content-Type", contentType)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	if strings.EqualFold(mediaType, "text/event-stream") {
		return w.cfg.EnableSSE && len(w.types) > 0
	}
	return w.types[strings.ToLower(mediaType)]
}

func (w *responseWriter) start(compress bool) error {
	w.decided = true
	addVary(w.Header())
	if compress {
		if value := w.pool.Get(); value != nil {
			w.writer = value.(Writer)
			w.writer.Reset(w.ResponseWriter)
		} else {
			var err error
			w.writer, err = w.cfg.NewWriter(w.ResponseWriter)
			if err != nil {
				return err
			}
		}
		h := w.Header()
		h.Set("Content-Encoding", w.cfg.Encoding)
		h.Del("Content-Length")
		h.Del("Accept-Ranges")
		h.Del("Content-MD5")
		h.Del("Content-Digest")
		if tag := h.Get("ETag"); strings.HasPrefix(tag, "\"") {
			h.Set("ETag", "W/"+tag)
		}
	}
	if len(w.pending) > 0 {
		p := w.pending
		w.pending = nil
		_, err := w.write(p)
		return err
	}
	return nil
}

func (w *responseWriter) write(p []byte) (int, error) {
	var n int
	var err error
	if w.writer != nil {
		n, err = w.writer.Write(p)
		w.accepted += n
	} else {
		n, err = w.ResponseWriter.Write(p)
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.decided {
		return w.write(p)
	}
	if len(p) == 0 {
		// Even an empty Write commits net/http headers. Preserve explicit status
		// codes without creating an empty compressed stream.
		if err := w.start(false); err != nil {
			return 0, err
		}
		return w.write(p)
	}
	sample := w.pending
	if len(sample) == 0 {
		sample = p
	}
	if !w.eligible(sample) {
		if err := w.start(false); err != nil {
			return 0, err
		}
	} else if !w.sse() && len(w.pending)+len(p) < w.cfg.MinLength {
		w.pending = append(w.pending, p...)
		return len(p), nil
	} else if err := w.start(true); err != nil {
		return 0, err
	}
	return w.write(p)
}

// ReadFrom preserves transport optimizations when negotiation disables compression.
// Otherwise all bytes still pass through Write and compression decisions.
func (w *responseWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.err != nil {
		return 0, w.err
	}
	if !w.enabled && !w.decided {
		if err := w.start(false); err != nil {
			return 0, err
		}
	}
	if w.decided && w.writer == nil {
		if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
			n, err := readerFrom.ReadFrom(r)
			if err != nil {
				w.err = err
			}
			return n, err
		}
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}

func (w *responseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// Explicit commits keep ordinary responses uncompressed. Opt-in SSE starts
// its encoder before committing headers, even before the first event.
func (w *responseWriter) DoWriteHeader() {
	if !w.decided {
		w.err = w.start(w.sse() && w.eligible(nil))
	}
	w.ResponseWriter.DoWriteHeader()
}

func (w *responseWriter) Flush() {
	if !w.decided {
		w.err = w.start(w.sse() && w.eligible(nil))
	}
	if w.writer != nil {
		if err := w.writer.Flush(); err != nil {
			w.err = err
		}
	}
	w.ResponseWriter.Flush()
}

func (w *responseWriter) finish() error {
	if !w.decided {
		if err := w.start(false); err != nil {
			return err
		}
	}
	if w.writer != nil {
		return errors.Join(w.err, w.writer.Close())
	}
	return w.err
}

func (w *responseWriter) release() {
	if w.writer != nil {
		// Reset clears references to the request writer before pooling, including
		// when unwinding a handler panic.
		w.writer.Reset(io.Discard)
		w.pool.Put(w.writer)
		w.writer = nil
	}
}
