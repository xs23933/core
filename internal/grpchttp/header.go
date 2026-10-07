// Package grpchttp defines the explicit response metadata contract shared by
// Core gRPC helpers and the HTTP Gateway. Unrelated metadata is never exposed.
package grpchttp

import (
	"strings"

	"golang.org/x/net/http/httpguts"
)

const HeaderPrefix = "core-http-"

// HeaderKey uses binary metadata so HTTP values need not be printable ASCII.
// grpc-go handles wire base64 encoding; callers pass the original value.
func HeaderKey(name string) string { return HeaderPrefix + strings.ToLower(name) + "-bin" }

func HeaderName(key string) (string, bool) {
	if !strings.HasPrefix(key, HeaderPrefix) || !strings.HasSuffix(key, "-bin") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(key, HeaderPrefix), "-bin")
	return name, ValidHeaderName(name)
}

func ValidHeaderName(name string) bool {
	if !httpguts.ValidHeaderFieldName(name) {
		return false
	}
	name = strings.ToLower(name)
	// HTTP tokens allow punctuation that gRPC metadata keys forbid. Restrict
	// names before embedding them in a key so stricter gRPC peers interoperate.
	for _, ch := range name {
		if ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	if strings.HasPrefix(name, "grpc-") {
		return false
	}
	switch name {
	case "connection", "keep-alive", "proxy-connection", "proxy-authenticate", "proxy-authorization",
		"transfer-encoding", "upgrade", "content-length", "content-type", "content-encoding", "te", "trailer":
		return false
	}
	return true
}

func ValidHeaderValue(value string) bool { return httpguts.ValidHeaderFieldValue(value) }
