package cors

import (
	"net/url"
	"strings"

	"github.com/xs23933/core/v3"
)

type Config struct {
	AllowOrigins     string // 例如: "http://localhost:3000"
	AllowHeaders     string // 默认为空，自动从请求中取
	AllowMethods     string // 默认为 "GET,POST,PUT,DELETE,OPTIONS"
	AllowCredentials bool   // 是否允许带 Cookie / Authorization
	ExposeHeaders    string // 允许浏览器读取的响应头
	MaxAge           string // 预检请求缓存时间（秒）
}

var defaultConfig = Config{
	AllowOrigins:     "*",
	AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS",
	AllowCredentials: false,
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
		if config[0].ExposeHeaders != "" {
			cfg.ExposeHeaders = config[0].ExposeHeaders
		}
		if config[0].MaxAge != "" {
			cfg.MaxAge = config[0].MaxAge
		}
		cfg.AllowCredentials = config[0].AllowCredentials
	}

	allowOrigins := parseOrigins(cfg.AllowOrigins)
	allowMethods := cfg.AllowMethods

	// OPTIONS 预检请求单独注册
	app.OPTIONS("/*", func(c core.Ctx) error {
		origin := c.GetHeader(core.HeaderOrigin)
		allowOrigin := matchOrigin(origin, allowOrigins, cfg.AllowCredentials)
		if allowOrigin == "" {
			return c.SendStatus(core.StatusForbidden)
		}

		c.Vary(core.HeaderOrigin)
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
		if cfg.MaxAge != "" {
			c.SetHeader(core.HeaderAccessControlMaxAge, cfg.MaxAge)
		}

		return c.SendStatus(core.StatusNoContent)
	})

	// 实际请求的跨域处理
	return func(c core.Ctx) error {
		origin := c.GetHeader(core.HeaderOrigin)
		allowOrigin := matchOrigin(origin, allowOrigins, cfg.AllowCredentials)
		if allowOrigin != "" {
			c.Vary(core.HeaderOrigin)
			c.SetHeader(core.HeaderAccessControlAllowOrigin, allowOrigin)
			if cfg.AllowCredentials {
				c.SetHeader(core.HeaderAccessControlAllowCredentials, "true")
			}
			if cfg.ExposeHeaders != "" {
				c.SetHeader(core.HeaderAccessControlExposeHeaders, cfg.ExposeHeaders)
			}
		}
		return c.Next()
	}
}

type originRule struct {
	raw      string
	wildcard bool
	host     string
}

func parseOrigins(origins string) []originRule {
	parts := strings.Split(origins, ",")
	rules := make([]originRule, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		if origin == "*" {
			rules = append(rules, originRule{raw: "*"})
			continue
		}

		host := origin
		wildcard := false
		if strings.Contains(origin, "://") {
			if u, err := url.Parse(origin); err == nil {
				host = u.Hostname()
			}
		}
		if strings.HasPrefix(host, "*.") {
			wildcard = true
			host = strings.TrimPrefix(host, "*.")
		}
		rules = append(rules, originRule{raw: origin, wildcard: wildcard, host: strings.ToLower(host)})
	}
	return rules
}

// matchOrigin 判断当前请求的 Origin 是否允许
func matchOrigin(origin string, allowOrigins []originRule, allowCredentials bool) string {
	if origin == "" {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	originHost := strings.ToLower(u.Hostname())

	for _, o := range allowOrigins {
		if o.raw == "*" {
			if allowCredentials {
				return ""
			}
			return "*"
		}
		if o.raw == origin {
			return origin
		}
		if o.wildcard && originHost != o.host && strings.HasSuffix(originHost, "."+o.host) {
			return origin
		}
	}
	return ""
}
