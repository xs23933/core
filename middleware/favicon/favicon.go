package favicon

import (
	"os"
	"strconv"

	"github.com/xs23933/core/v2"
)

const (
	HEADER_ALLOW = "GET, HEAD, OPTIONS"
)

type Config struct {
	File         string `json:"file"`
	Url          string `json:"url"`
	CacheControl string `json:"cache_control"`
}

var DefaultConfig = Config{
	File:         "favicon.ico",
	Url:          "/favicon.ico",
	CacheControl: "public, max-age=31536000",
}

func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		cfg = conf[0]
	}

	icon, err := os.ReadFile(cfg.File)
	if err != nil {
		core.Erro(err.Error())
	}
	iconLen := strconv.Itoa(len(icon))

	return func(c core.Ctx) error {
		if c.Path() != cfg.Url {
			return c.Next()
		}

		if c.Method() != core.MethodGet && c.Method() != core.MethodHead {
			if c.Method() != core.MethodOptions {
				c.Status(core.StatusMethodNotAllowed)
			} else {
				c.Status(core.StatusOK)
			}
			c.SetHeader(core.HeaderAllow, HEADER_ALLOW)
			c.SetHeader(core.HeaderContentLength, "0")
			return nil
		}
		// Serve cached favicon
		if len(icon) > 0 {
			c.SetHeader(core.HeaderContentLength, iconLen)
			c.SetHeader(core.HeaderContentType, core.MIME("ico"))
			c.SetHeader(core.HeaderCacheControl, cfg.CacheControl)
			c.Send(icon)
		}

		return c.SendStatus(core.StatusNoContent)
	}
}
