// Package compress negotiates zstd and gzip response compression. It reuses the
// standalone middleware's streaming writers, thresholds and compressor pools.
package compress

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	zstdcodec "github.com/klauspost/compress/zstd"
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/middleware/gzip"
	"github.com/xs23933/core/v3/middleware/zstd"
)

// Config shares response eligibility settings across both encoders.
// Zero values retain the standalone middleware defaults.
type Config struct {
	ContentTypes []string               // nil uses defaults; a non-nil empty slice disables compression.
	MinLength    int                    // Default 1024 bytes; negative disables the threshold.
	GzipLevel    int                    // compress/gzip level; zero uses DefaultCompression.
	ZstdLevel    zstdcodec.EncoderLevel // Zero uses SpeedDefault.
	Skip         func(core.Ctx) bool
}

// New selects the highest positive q weight, preferring zstd on ties. An
// explicit identity entry can outrank compression; implicit identity is a
// fallback. Missing/empty Accept-Encoding retains uncompressed responses.
// Install this once, instead of stacking the standalone gzip/zstd middleware.
func New(config ...Config) core.HandlerFunc {
	var cfg Config
	if len(config) > 0 {
		cfg = config[0]
	}
	if err := validate(cfg); err != nil {
		return func(core.Ctx) error { return err }
	}
	gzipHandler := gzip.New(gzip.Config{ContentTypes: cfg.ContentTypes, MinLength: cfg.MinLength, Level: cfg.GzipLevel})
	zstdHandler := zstd.New(zstd.Config{ContentTypes: cfg.ContentTypes, MinLength: cfg.MinLength, Level: cfg.ZstdLevel})
	return func(c core.Ctx) error {
		if cfg.Skip != nil && cfg.Skip(c) {
			return c.Next()
		}
		original := c.Response()
		if original.Written() {
			return c.Next()
		}
		addVary(original.Header())
		coding, identityAllowed := negotiate(c.Request().Header.Values("Accept-Encoding"))
		w := &responseWriter{ResponseWriter: original, identityAllowed: identityAllowed, head: c.Method() == http.MethodHead}
		c.SetResponseWriter(w)
		defer c.SetResponseWriter(original)
		if coding == "" && !identityAllowed && !hasEncoding(original.Header()) {
			w.reject()
			return w.err
		}
		var err error
		switch coding {
		case "zstd":
			err = zstdHandler(c)
		case "gzip":
			err = gzipHandler(c)
		default:
			err = c.Next()
		}
		if !identityAllowed && !w.Written() {
			if err == nil {
				w.DoWriteHeader()
			} else if !w.head && !hasEncoding(w.Header()) {
				w.reject()
			}
		}
		return errors.Join(withoutRejection(err), w.err)
	}
}

var errNotAcceptable = errors.New("compress: representation not acceptable")

// Policy refusal stops producers but is not a server error. Keep unrelated
// errors returned alongside it by a handler or an encoder's Close.
func withoutRejection(err error) error {
	if !errors.Is(err, errNotAcceptable) {
		return err
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		children := wrapped.Unwrap()
		kept := make([]error, 0, len(children))
		for _, child := range children {
			if next := withoutRejection(child); next != nil {
				kept = append(kept, next)
			}
		}
		return errors.Join(kept...)
	case interface{ Unwrap() error }:
		return withoutRejection(wrapped.Unwrap())
	default:
		return nil
	}
}

func validate(cfg Config) error {
	for _, value := range cfg.ContentTypes {
		if _, _, err := mime.ParseMediaType(value); err != nil {
			return fmt.Errorf("compress content type %q: %w", value, err)
		}
	}
	if cfg.GzipLevel < -2 || cfg.GzipLevel > 9 {
		return fmt.Errorf("compress gzip level: %d", cfg.GzipLevel)
	}
	if cfg.ZstdLevel < 0 || cfg.ZstdLevel > zstdcodec.SpeedBestCompression {
		return fmt.Errorf("compress zstd level: %d", cfg.ZstdLevel)
	}
	return nil
}

// Unspecified codings inherit wildcard weights. Duplicate declarations use the
// minimum weight, so an explicit prohibition cannot be undone by field order.
func negotiate(values []string) (coding string, identityAllowed bool) {
	weights := [4]int{-1, -1, -1, -1} // zstd, gzip, identity, wildcard
	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			name, params, hasParams := strings.Cut(strings.TrimSpace(item), ";")
			index := -1
			switch {
			case strings.EqualFold(strings.TrimSpace(name), "zstd"):
				index = 0
			case strings.EqualFold(strings.TrimSpace(name), "gzip"):
				index = 1
			case strings.EqualFold(strings.TrimSpace(name), "identity"):
				index = 2
			case strings.TrimSpace(name) == "*":
				index = 3
			}
			if index < 0 {
				continue
			}
			weight := 1000
			if hasParams {
				key, value, ok := strings.Cut(strings.TrimSpace(params), "=")
				if !ok || !strings.EqualFold(key, "q") {
					weight = 0
				} else {
					weight = quality(strings.TrimSpace(value))
				}
			}
			if weights[index] < 0 || weight < weights[index] {
				weights[index] = weight
			}
		}
	}
	zq, gq := weights[0], weights[1]
	if zq < 0 {
		zq = weights[3]
	}
	if gq < 0 {
		gq = weights[3]
	}
	identityAllowed = weights[2] > 0 || (weights[2] < 0 && weights[3] != 0)
	best := 0
	if zq > 0 {
		coding = "zstd"
		best = zq
	}
	if gq > best {
		coding = "gzip"
		best = gq
	}
	if weights[2] > best {
		coding = ""
	}
	return coding, identityAllowed
}

// RFC 9110 qvalue is 0..1 with at most three fractional digits. Malformed
// parameters make that coding unacceptable rather than silently enabling it.
func quality(value string) int {
	if value == "0" {
		return 0
	}
	if value == "1" {
		return 1000
	}
	if len(value) < 2 || len(value) > 5 || value[1] != '.' || (value[0] != '0' && value[0] != '1') {
		return 0
	}
	result := 0
	factor := 100
	for i := 2; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' || (value[0] == '1' && value[i] != '0') {
			return 0
		}
		result += int(value[i]-'0') * factor
		factor /= 10
	}
	if value[0] == '1' {
		return 1000
	}
	return result
}

// Only the negotiation boundary is wrapped here. Compression and transport
// lifecycles remain owned by the existing standalone writers.
type responseWriter struct {
	core.ResponseWriter
	identityAllowed bool
	head            bool
	rejected        bool
	err             error
}

func addVary(h http.Header) {
	for _, line := range h.Values("Vary") {
		for value := range strings.SplitSeq(line, ",") {
			if strings.EqualFold(strings.TrimSpace(value), "Accept-Encoding") || strings.TrimSpace(value) == "*" {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}

func hasEncoding(h http.Header) bool {
	encoding := strings.TrimSpace(h.Get("Content-Encoding"))
	return encoding != "" && !strings.EqualFold(encoding, "identity")
}

func (w *responseWriter) prepare() {
	addVary(w.Header())
	status := w.Status()
	if w.rejected || w.ResponseWriter.Written() || w.identityAllowed || w.head || status < 200 || status == 204 || status == 304 || hasEncoding(w.Header()) {
		return
	}
	w.reject()
}

func (w *responseWriter) reject() {
	if w.rejected {
		return
	}
	w.rejected = true
	// The handler's representation was refused; retain unrelated headers/cookies.
	for _, name := range []string{"Content-Length", "Content-Encoding", "Content-Range", "Accept-Ranges", "Content-MD5", "Content-Digest", "ETag", "Last-Modified", "Content-Disposition", "Cache-Control"} {
		w.Header().Del(name)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(http.StatusNotAcceptable)
	if w.head {
		w.ResponseWriter.DoWriteHeader()
		return
	}
	_, w.err = w.ResponseWriter.Write([]byte("Not Acceptable\n"))
}

func (w *responseWriter) WriteHeader(code int) {
	if w.rejected {
		return
	}
	addVary(w.Header())
	w.ResponseWriter.WriteHeader(code)
}
func (w *responseWriter) Write(p []byte) (int, error) {
	w.prepare()
	if w.rejected {
		if w.err != nil {
			return 0, w.err
		}
		return 0, errNotAcceptable
	}
	return w.ResponseWriter.Write(p)
}
func (w *responseWriter) WriteString(s string) (int, error) {
	w.prepare()
	if w.rejected {
		if w.err != nil {
			return 0, w.err
		}
		return 0, errNotAcceptable
	}
	return w.ResponseWriter.WriteString(s)
}
func (w *responseWriter) DoWriteHeader() { w.prepare(); w.ResponseWriter.DoWriteHeader() }
func (w *responseWriter) Flush()         { w.DoWriteHeader(); w.ResponseWriter.Flush() }
func (w *responseWriter) ReadFrom(r io.Reader) (int64, error) {
	w.prepare()
	if w.rejected {
		if w.err != nil {
			return 0, w.err
		}
		return 0, errNotAcceptable
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}
