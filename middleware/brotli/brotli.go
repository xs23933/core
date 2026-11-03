// Package brotli provides opt-in streaming Brotli HTTP response compression.
package brotli

import (
	"fmt"
	"io"

	"github.com/andybalholm/brotli"
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/internal/responsecompression"
)

type Config struct {
	ContentTypes []string // nil uses defaults; non-nil empty disables compression.
	Level        int      // Zero uses DefaultCompression (6); explicit levels are 1..11.
	MinLength    int      // Zero uses 1024 bytes; negative disables the threshold.
	EnableSSE    bool     // Opt into event-level compression/Flush, bypassing MinLength.
	Skip         func(core.Ctx) bool
}

var DefaultConfig = Config{
	ContentTypes: []string{"application/json", "text/html", "text/css", "text/javascript", "application/javascript", "text/plain", "application/xml", "text/xml", "image/svg+xml"},
	Level:        brotli.DefaultCompression,
	MinLength:    1024,
}

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
		cfg.EnableSSE = v.EnableSSE
		cfg.Skip = v.Skip
	}
	return responsecompression.New(responsecompression.Config{
		Encoding: "br", ContentTypes: cfg.ContentTypes, MinLength: cfg.MinLength, EnableSSE: cfg.EnableSSE, Skip: cfg.Skip,
		NewWriter: func(dst io.Writer) (responsecompression.Writer, error) {
			if cfg.Level < brotli.BestSpeed || cfg.Level > brotli.BestCompression {
				return nil, fmt.Errorf("invalid brotli level %d", cfg.Level)
			}
			return brotli.NewWriterLevel(dst, cfg.Level), nil
		},
	})
}
