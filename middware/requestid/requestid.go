package requestid

import (
	"github.com/xs23933/core/v2"
	"github.com/xs23933/uid"
)

type Config struct {
	Header     string
	ContextKey string
}

var DefaultConfig = Config{
	Header:     core.HeaderXRequestID,
	ContextKey: "requestid",
}

func New(conf ...Config) core.HandlerFunc {
	cfg := DefaultConfig
	if len(conf) > 0 {
		cfg = conf[0]
	}
	return func(c core.Ctx) error {
		rid := c.GetHeader(cfg.Header, uid.New().String())
		c.SetHeader(cfg.Header, rid)

		c.Set(cfg.ContextKey, rid)
		return c.Next()
	}
}
