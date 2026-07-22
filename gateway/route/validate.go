package route

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Validate checks that a definition can be unambiguously published and
// dispatched. It does not mutate the definition.
func Validate(definition *Definition) error {
	if definition == nil {
		return errors.New("route: definition is nil")
	}
	method := strings.ToUpper(strings.TrimSpace(definition.Method))
	if method == "" {
		return errors.New("route: method is required")
	}
	if !supportedHTTPMethod(method) {
		return fmt.Errorf("route: unsupported method %s", method)
	}
	if _, err := CanonicalPath(definition.Path); err != nil {
		return fmt.Errorf("route: public path: %w", err)
	}
	if strings.TrimSpace(definition.ServiceName) == "" {
		return errors.New("route: service_name is required")
	}

	protocol := definition.Protocol
	if protocol == "" {
		protocol = ProtocolGRPC
	}
	switch protocol {
	case ProtocolHTTP:
		upstreamPath := normalizeDefinitionPath(definition.UpstreamPath)
		if upstreamPath != "" {
			if _, err := CanonicalPath(upstreamPath); err != nil {
				return fmt.Errorf("route: upstream path: %w", err)
			}
			if upstreamPath != normalizeDefinitionPath(definition.Path) {
				return errors.New("route: upstream_path rewrite is not supported for http route")
			}
		}
		if len(definition.Headers) != 0 {
			return errors.New("route: static headers are not supported for http route")
		}
	case ProtocolGRPC:
		if strings.TrimSpace(definition.GRPCMethod) == "" {
			return errors.New("route: grpc_method is required for grpc route")
		}
	default:
		return fmt.Errorf("route: unsupported protocol %s", protocol)
	}

	return nil
}

func supportedHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
		http.MethodConnect, http.MethodTrace:
		return true
	default:
		return false
	}
}
