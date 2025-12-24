package cors

import (
	"strings"

	"github.com/xs23933/core/v3"
)

type Config struct {
	AllowOrigins     string // 例如: "http://localhost:3000"
	AllowHeaders     string // 默认为空，自动从请求中取
	AllowMethods     string // 默认为 "GET,POST,PUT,DELETE,OPTIONS"
	AllowCredentials bool   // 是否允许带 Cookie / Authorization
}

var defaultConfig = Config{
	AllowOrigins:     "*",
	AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS",
	AllowCredentials: true,
}

// New 返回一个标准 CORS 中间件
func New(app *core.Core, config ...Config) core.HandlerFunc {
	cfg := defaultConfig
	if len(config) > 0 {
		if config[0].AllowOrigins != "" {
			cfg.AllowOrigins = config[0].AllowOrigins
		}
		if config[0].AllowHeaders != "" {
			cfg.AllowHeaders = config[0].AllowHeaders
		}
		if config[0].AllowMethods != "" {
			cfg.AllowMethods = config[0].AllowMethods
		}
		cfg.AllowCredentials = config[0].AllowCredentials
	}

	allowOrigins := strings.Split(strings.ReplaceAll(cfg.AllowOrigins, " ", ""), ",")
	allowMethods := cfg.AllowMethods

	// OPTIONS 预检请求单独注册
	app.OPTIONS("/*", func(c core.Ctx) error {
		origin := c.GetHeader(core.HeaderOrigin)
		allowOrigin := matchOrigin(origin, allowOrigins)
		if allowOrigin == "" {
			return c.SendStatus(core.StatusForbidden)
		}

		c.SetHeader(core.HeaderAccessControlAllowOrigin, allowOrigin)
		c.SetHeader(core.HeaderAccessControlAllowMethods, allowMethods)

		if cfg.AllowCredentials {
			c.SetHeader(core.HeaderAccessControlAllowCredentials, "true")
		}

		if cfg.AllowHeaders != "" {
			c.SetHeader(core.HeaderAccessControlAllowHeaders, cfg.AllowHeaders)
		} else {
			h := c.GetHeader(core.HeaderAccessControlRequestHeaders)
			if h != "" {
				c.SetHeader(core.HeaderAccessControlAllowHeaders, h)
			}
		}

		return c.SendStatus(core.StatusNoContent)
	})

	// 实际请求的跨域处理
	return func(c core.Ctx) error {
		origin := c.GetHeader(core.HeaderOrigin)
		allowOrigin := matchOrigin(origin, allowOrigins)
		if allowOrigin != "" {
			c.SetHeader(core.HeaderAccessControlAllowOrigin, allowOrigin)
			if cfg.AllowCredentials {
				c.SetHeader(core.HeaderAccessControlAllowCredentials, "true")
			}
		}
		return c.Next()
	}
}

// matchOrigin 判断当前请求的 Origin 是否允许
func matchOrigin(origin string, allowOrigins []string) string {
	if origin == "" {
		return ""
	}
	for _, o := range allowOrigins {
		if o == "*" {
			return "*"
		}
		if o == origin {
			return origin
		}
		// 通配符匹配
		if strings.HasPrefix(o, "*.") {
			// *.example.com 匹配任意子域
			domain := strings.TrimPrefix(o, "*.")
			if strings.HasSuffix(origin, domain) {
				return origin
			}
		}
	}
	return ""
}
