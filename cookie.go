package core

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Shared by HTTP Ctx and gRPC helpers to preserve the same option semantics.
func newCookie(name, value string, exp time.Time, path, domain string, args ...any) *http.Cookie {
	if path == "" {
		path = "/"
	}
	cookie := &http.Cookie{Name: name, Value: url.QueryEscape(value), Expires: exp, Path: path, SameSite: http.SameSiteLaxMode}
	for _, arg := range args {
		switch a := arg.(type) {
		case string:
			if strings.EqualFold(a, "httponly") {
				cookie.HttpOnly = true
			} else {
				cookie.Domain = a
			}
		case bool:
			cookie.Secure = a
		}
	}
	if cookie.Domain == "" {
		cookie.Domain = domain
	}
	return cookie
}

func removedCookie(name, path, domain string, dom ...string) *http.Cookie {
	cookie := &http.Cookie{Name: name, Value: "", Expires: time.Now().Add(-time.Hour), Path: path}
	if len(dom) > 0 {
		cookie.Domain = dom[0]
	}
	if cookie.Domain == "" {
		cookie.Domain = domain
	}
	return cookie
}
