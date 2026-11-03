// Package gzip provides opt-in streaming HTTP response compression.
package gzip

import (
	"compress/gzip"
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/internal/responsecompression"
	"io"
)

// Config configures compression. Zero fields use DefaultConfig values.
type Config struct {
	EnableSSE bool // Opt into text/event-stream compression with event-level Flush.
	// ContentTypes replaces the defaults when non-nil. An empty slice disables
	// compression. Matching ignores case and parameters such as charset.
	ContentTypes []string
	// Level is a compress/gzip level. Zero uses gzip.DefaultCompression.
	Level int
	// MinLength defaults to 1024 bytes. A negative value disables the threshold.
	MinLength int
	Skip      func(core.Ctx) bool
}

var DefaultConfig = Config{
	ContentTypes: []string{
		"application/json", "text/html", "text/css", "text/javascript",
		"application/javascript", "text/plain", "application/xml", "text/xml", "image/svg+xml",
	},
	Level:     gzip.DefaultCompression,
	MinLength: 1024,
}

// New compresses eligible responses. SSE is uncompressed unless EnableSSE is
// true, in which case each event Flush also flushes the encoder.
func New(config ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(config) > 0 {
		v := config[0]
		if v.ContentTypes != nil {
			cfg.ContentTypes = v.ContentTypes
		}
		if v.Level != 0 {
			cfg.Level = v.Level
		}
		if v.MinLength != 0 {
			cfg.MinLength = v.MinLength
		}
		cfg.Skip = v.Skip
		cfg.EnableSSE = v.EnableSSE
	}
	return responsecompression.New(responsecompression.Config{
		Encoding: "gzip", ContentTypes: cfg.ContentTypes, MinLength: cfg.MinLength, EnableSSE: cfg.EnableSSE, Skip: cfg.Skip,
		NewWriter: func(dst io.Writer) (responsecompression.Writer, error) { return gzip.NewWriterLevel(dst, cfg.Level) },
	})
}
