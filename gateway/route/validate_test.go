package route

import (
	"net/http"
	"reflect"
	"testing"
)

func minimalHTTPDefinition() *Definition {
	return &Definition{
		Protocol:    ProtocolHTTP,
		Method:      "GET",
		Path:        "/api/users/:id",
		ServiceName: "users",
	}
}

func TestValidateMinimalHTTP(t *testing.T) {
	t.Run("method path and service are sufficient", func(t *testing.T) {
		if err := Validate(minimalHTTPDefinition()); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})
	t.Run("path-equal upstream path remains compatible", func(t *testing.T) {
		definition := minimalHTTPDefinition()
		definition.UpstreamPath = definition.Path
		if err := Validate(definition); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})
	t.Run("differing upstream path is rejected", func(t *testing.T) {
		definition := minimalHTTPDefinition()
		definition.UpstreamPath = "/internal/users/:id"
		if err := Validate(definition); err == nil {
			t.Fatal("Validate() error = nil, want unsupported rewrite error")
		}
	})
	t.Run("static headers are rejected", func(t *testing.T) {
		definition := minimalHTTPDefinition()
		definition.UpstreamPath = definition.Path
		definition.Headers = map[string]string{"X-Gateway": "core"}
		if err := Validate(definition); err == nil {
			t.Fatal("Validate() error = nil, want unsupported static headers error")
		}
	})
	t.Run("parameters are absent from the public contract", func(t *testing.T) {
		if _, exists := reflect.TypeOf(Definition{}).FieldByName("Parameters"); exists {
			t.Fatal("Definition still exposes Parameters")
		}
	})
}

func TestValidateRejectsInvalidRouteDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Definition)
	}{
		{"empty method", func(route *Definition) { route.Method = "" }},
		{"unsupported method", func(route *Definition) { route.Method = "BREW" }},
		{"non-absolute public path", func(route *Definition) { route.Path = "api/users" }},
		{"empty service name", func(route *Definition) { route.ServiceName = "" }},
		{"gRPC route without gRPC method", func(route *Definition) {
			route.Protocol = ProtocolGRPC
			route.GRPCMethod = ""
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			route := minimalHTTPDefinition()
			test.mutate(route)
			if err := Validate(route); err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
		})
	}
}

func TestValidateAcceptsCoreConcreteHTTPMethods(t *testing.T) {
	for _, method := range []string{http.MethodConnect, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			definition := minimalHTTPDefinition()
			definition.Method = method
			if err := Validate(definition); err != nil {
				t.Fatalf("Validate(%s) error = %v", method, err)
			}
		})
	}
}
