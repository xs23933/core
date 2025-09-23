package cros

import (
	"strconv"
	"strings"

	"github.com/xs23933/core/v2"
)

type Config struct {
	AllowOrigins     string // default value "*"
	AllowHeaders     string // default value ""
	AllowCredentials bool   // default false
	AllowMethods     string // default "POST, GET, OPTIONS, PUT, DELETE"
}

var defaultConfig = Config{
	AllowOrigins:     "*",
	AllowHeaders:     "",
	AllowCredentials: true,
	AllowMethods: strings.Join([]string{
		core.MethodGet,
		core.MethodPost,
		core.MethodHead,
		core.MethodPut,
		core.MethodDelete,
		core.MethodPatch,
	}, ","),
}

func New(co *core.Core, config ...Config) core.HandlerFunc {
	cfg := defaultConfig

	if len(config) > 0 {
		cfg = config[0]
		if cfg.AllowOrigins == "" {
			cfg.AllowOrigins = defaultConfig.AllowOrigins
		}
	}
	// Convert string to slice
	allowOrigins := strings.Split(strings.ReplaceAll(cfg.AllowOrigins, " ", ""), ",")
	// Strip white spaces
	allowMethods := strings.ReplaceAll(cfg.AllowMethods, " ", "")
	allowHeaders := strings.ReplaceAll(cfg.AllowHeaders, " ", "")
	allowCredentials := "false"
	if cfg.AllowCredentials {
		allowCredentials = "true"
	}

	// 显式注册OPTIONS路由处理预检请求
	co.OPTIONS("/*", func(c core.Ctx) error {
		return handlePreflight(c, allowOrigins, allowMethods, allowHeaders, cfg.AllowCredentials)
	})

	return func(c core.Ctx) error {
		core.Erro("method: %v : %v", c.Method(), core.MethodOptions)
		if c.Request().Method != core.MethodOptions {
			core.Erro("Wtf cros")
			c.SetHeader(core.HeaderAccessControlAllowOrigin, strings.Join(allowOrigins, ","))
			c.SetHeader(core.HeaderAccessControlAllowMethods, allowMethods)
			c.SetHeader(core.HeaderAccessControlAllowCredentials, allowCredentials)
			return c.Next()
		}
		return nil
	}
}

func handlePreflight(c core.Ctx, allowOrigins []string, allowMethods, allowHeaders string, allowCredentials bool) error {
	origin := c.GetHeader(core.HeaderOrigin)
	allowOrigin := ""
	for _, o := range allowOrigins {
		if o == "*" || o == origin || matchSubdomain(origin, o) {
			allowOrigin = o
			break
		}
	}

	if allowOrigin == "" {
		return c.SendStatus(core.StatusForbidden)
	}

	c.SetHeader(core.HeaderAccessControlAllowOrigin, allowOrigin)
	c.SetHeader(core.HeaderAccessControlAllowMethods, allowMethods)
	c.SetHeader(core.HeaderAccessControlAllowCredentials, strconv.FormatBool(allowCredentials))

	if allowHeaders != "" {
		c.SetHeader(core.HeaderAccessControlAllowHeaders, allowHeaders)
	} else {
		h := c.GetHeader(core.HeaderAccessControlRequestHeaders)
		if h != "" {
			c.SetHeader(core.HeaderAccessControlAllowHeaders, h)
		}
	}

	return c.SendStatus(core.StatusNoContent)
}

func matchScheme(domain, pattern string) bool {
	didx := strings.Index(domain, ":")
	pidx := strings.Index(pattern, ":")
	return didx != -1 && pidx != -1 && domain[:didx] == pattern[:pidx]
}

// matchSubdomain compares authority with wildcard
func matchSubdomain(domain, pattern string) bool {
	if !matchScheme(domain, pattern) {
		return false
	}
	didx := strings.Index(domain, "://")
	pidx := strings.Index(pattern, "://")
	if didx == -1 || pidx == -1 {
		return false
	}
	domAuth := domain[didx+3:]
	// to avoid long loop by invalid long domain
	const maxDomainLen = 253
	if len(domAuth) > maxDomainLen {
		return false
	}
	patAuth := pattern[pidx+3:]

	domComp := strings.Split(domAuth, ".")
	patComp := strings.Split(patAuth, ".")
	const divHalf = 2
	for i := len(domComp)/divHalf - 1; i >= 0; i-- {
		opp := len(domComp) - 1 - i
		domComp[i], domComp[opp] = domComp[opp], domComp[i]
	}
	for i := len(patComp)/divHalf - 1; i >= 0; i-- {
		opp := len(patComp) - 1 - i
		patComp[i], patComp[opp] = patComp[opp], patComp[i]
	}

	for i, v := range domComp {
		if len(patComp) <= i {
			return false
		}
		p := patComp[i]
		if p == "*" {
			return true
		}
		if p != v {
			return false
		}
	}
	return false
}
