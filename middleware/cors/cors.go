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

type compiledConfig struct {
	allowAny         bool
	allowCredentials bool
	exactOrigins     map[string]struct{}
	wildcardOrigins  []originRule
	allowMethods     map[string]struct{}
	allowHeaders     map[string]struct{}
	allowMethodsRaw  string
	allowHeadersRaw  string
	exposeHeaders    string
	maxAge           string
}

type originRule struct {
	any      bool
	wildcard bool
	scheme   string
	host     string
	port     string
	exact    string
}

type parsedOrigin struct {
	scheme string
	host   string
	port   string
	exact  string
}

// New returns a side-effect-free CORS middleware.
func New(config ...Config) core.HandlerFunc {
	cfg := defaultConfig
	if len(config) > 0 {
		cfg = mergeConfig(cfg, config[0])
	}
	policy := compileConfig(cfg)
	return policy.handle
}

// NewWithApp constructs CORS middleware and registers a legacy catch-all
// preflight route.
// Deprecated: use app.Use(cors.New(config...)).
func NewWithApp(app *core.Core, config ...Config) core.HandlerFunc {
	cfg := defaultConfig
	if len(config) > 0 {
		cfg = mergeConfig(cfg, config[0])
	}
	policy := compileConfig(cfg)
	app.OPTIONS("/*", policy.handleLegacyPreflight)
	return policy.handle
}

func mergeConfig(base, override Config) Config {
	if override.AllowOrigins != "" {
		base.AllowOrigins = override.AllowOrigins
	}
	if override.AllowHeaders != "" {
		base.AllowHeaders = override.AllowHeaders
	}
	if override.AllowMethods != "" {
		base.AllowMethods = override.AllowMethods
	}
	if override.ExposeHeaders != "" {
		base.ExposeHeaders = override.ExposeHeaders
	}
	if override.MaxAge != "" {
		base.MaxAge = override.MaxAge
	}
	base.AllowCredentials = override.AllowCredentials
	return base
}

func compileConfig(cfg Config) compiledConfig {
	policy := compiledConfig{
		allowCredentials: cfg.AllowCredentials,
		exactOrigins:     make(map[string]struct{}),
		allowMethods:     compileSet(cfg.AllowMethods, strings.ToUpper),
		allowHeaders:     compileSet(cfg.AllowHeaders, strings.ToLower),
		allowMethodsRaw:  joinList(cfg.AllowMethods),
		allowHeadersRaw:  joinList(cfg.AllowHeaders),
		exposeHeaders:    joinList(cfg.ExposeHeaders),
		maxAge:           strings.TrimSpace(cfg.MaxAge),
	}

	for _, rule := range parseOrigins(cfg.AllowOrigins) {
		switch {
		case rule.any:
			policy.allowAny = true
		case rule.wildcard:
			policy.wildcardOrigins = append(policy.wildcardOrigins, rule)
		case rule.exact != "":
			policy.exactOrigins[rule.exact] = struct{}{}
		}
	}

	return policy
}

func compileSet(value string, normalize func(string) string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result[normalize(item)] = struct{}{}
		}
	}
	return result
}

func joinList(value string) string {
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return strings.Join(result, ",")
}

func parseOrigins(origins string) []originRule {
	parts := strings.Split(origins, ",")
	rules := make([]originRule, 0, len(parts))
	for _, part := range parts {
		if rule, ok := parseOriginRule(strings.TrimSpace(part)); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

func parseOriginRule(value string) (originRule, bool) {
	if value == "*" {
		return originRule{any: true}, true
	}
	if value == "" {
		return originRule{}, false
	}

	if !strings.Contains(value, "://") {
		if !strings.HasPrefix(value, "*.") {
			return originRule{}, false
		}
		host := strings.ToLower(strings.TrimPrefix(value, "*."))
		if host == "" || strings.ContainsAny(host, "/:?#@") {
			return originRule{}, false
		}
		return originRule{wildcard: true, host: host}, true
	}

	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return originRule{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return originRule{}, false
	}

	rule := originRule{
		scheme: strings.ToLower(u.Scheme),
		host:   host,
		port:   u.Port(),
	}
	if strings.HasPrefix(rule.host, "*.") {
		rule.wildcard = true
		rule.host = strings.TrimPrefix(rule.host, "*.")
		if rule.host == "" {
			return originRule{}, false
		}
		return rule, true
	}
	rule.exact = canonicalOrigin(rule.scheme, rule.host, rule.port)
	return rule, true
}

func parseRequestOrigin(value string) (parsedOrigin, bool) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return parsedOrigin{}, false
	}
	origin := parsedOrigin{
		scheme: strings.ToLower(u.Scheme),
		host:   strings.ToLower(u.Hostname()),
		port:   u.Port(),
	}
	if origin.host == "" {
		return parsedOrigin{}, false
	}
	origin.exact = canonicalOrigin(origin.scheme, origin.host, origin.port)
	return origin, true
}

func canonicalOrigin(scheme, host, port string) string {
	if port != "" {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

func (cfg compiledConfig) matchOrigin(origin string) string {
	if origin == "" || (cfg.allowAny && cfg.allowCredentials) {
		return ""
	}
	if cfg.allowAny {
		return "*"
	}

	parsed, ok := parseRequestOrigin(origin)
	if !ok {
		return ""
	}
	if _, ok := cfg.exactOrigins[parsed.exact]; ok {
		return origin
	}
	for _, rule := range cfg.wildcardOrigins {
		if rule.scheme != "" && rule.scheme != parsed.scheme {
			continue
		}
		if rule.port != "" && rule.port != parsed.port {
			continue
		}
		if parsed.host != rule.host && strings.HasSuffix(parsed.host, "."+rule.host) {
			return origin
		}
	}
	return ""
}

func (cfg compiledConfig) handle(c core.Ctx) error {
	origin := c.GetHeader(core.HeaderOrigin)
	if origin == "" {
		return c.Next()
	}

	if c.Method() == core.MethodOptions {
		requestMethod := strings.TrimSpace(c.GetHeader(core.HeaderAccessControlRequestMethod))
		if requestMethod == "" || !core.IsRouteFallback(c) {
			return cfg.handleActual(c, origin)
		}
		return cfg.handlePreflight(c, origin, requestMethod)
	}

	return cfg.handleActual(c, origin)
}

func (cfg compiledConfig) handleLegacyPreflight(c core.Ctx) error {
	origin := c.GetHeader(core.HeaderOrigin)
	requestMethod := strings.TrimSpace(c.GetHeader(core.HeaderAccessControlRequestMethod))
	if origin == "" || requestMethod == "" {
		return c.Next()
	}
	return cfg.handlePreflight(c, origin, requestMethod)
}

func (cfg compiledConfig) handlePreflight(c core.Ctx, origin, requestMethod string) error {
	allowOrigin := cfg.matchOrigin(origin)
	if allowOrigin == "" {
		return writeStatus(c, core.StatusForbidden)
	}

	requestMethod = strings.ToUpper(requestMethod)
	if _, ok := cfg.allowMethods[requestMethod]; !ok {
		return writeStatus(c, core.StatusForbidden)
	}

	requestHeaders := c.GetHeader(core.HeaderAccessControlRequestHeaders)
	if len(cfg.allowHeaders) > 0 && !cfg.headersAllowed(requestHeaders) {
		return writeStatus(c, core.StatusForbidden)
	}

	c.SetHeader(core.HeaderAccessControlAllowOrigin, allowOrigin)
	c.SetHeader(core.HeaderAccessControlAllowMethods, cfg.allowMethodsRaw)
	if cfg.allowCredentials {
		c.SetHeader(core.HeaderAccessControlAllowCredentials, "true")
	}
	if cfg.allowHeadersRaw != "" {
		c.SetHeader(core.HeaderAccessControlAllowHeaders, cfg.allowHeadersRaw)
	} else if requestHeaders != "" {
		c.SetHeader(core.HeaderAccessControlAllowHeaders, requestHeaders)
		vary(c, core.HeaderAccessControlRequestHeaders)
	}
	if cfg.maxAge != "" {
		c.SetHeader(core.HeaderAccessControlMaxAge, cfg.maxAge)
	}
	if allowOrigin != "*" {
		vary(c, core.HeaderOrigin)
	}
	vary(c, core.HeaderAccessControlRequestMethod)
	return writeStatus(c, core.StatusNoContent)
}

func (cfg compiledConfig) handleActual(c core.Ctx, origin string) error {
	allowOrigin := cfg.matchOrigin(origin)
	if allowOrigin == "" {
		return c.Next()
	}

	c.SetHeader(core.HeaderAccessControlAllowOrigin, allowOrigin)
	if cfg.allowCredentials {
		c.SetHeader(core.HeaderAccessControlAllowCredentials, "true")
	}
	if cfg.exposeHeaders != "" {
		c.SetHeader(core.HeaderAccessControlExposeHeaders, cfg.exposeHeaders)
	}
	if allowOrigin != "*" {
		vary(c, core.HeaderOrigin)
	}
	return c.Next()
}

func (cfg compiledConfig) headersAllowed(value string) bool {
	for _, header := range strings.Split(value, ",") {
		header = strings.ToLower(strings.TrimSpace(header))
		if header == "" {
			continue
		}
		if _, ok := cfg.allowHeaders[header]; !ok {
			return false
		}
	}
	return true
}

func vary(c core.Ctx, field string) {
	for _, value := range c.Response().Header().Values(core.HeaderVary) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), field) {
				return
			}
		}
	}
	c.Vary(field)
}

func writeStatus(c core.Ctx, status int) error {
	c.Status(status)
	c.Response().DoWriteHeader()
	return nil
}
