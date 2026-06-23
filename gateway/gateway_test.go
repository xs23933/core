package gateway

import (
	"encoding/base64"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGatewayConnectRetryDelayCapsAtFiveSeconds(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 0},
		{attempt: 1, want: 500 * time.Millisecond},
		{attempt: 4, want: 2 * time.Second},
		{attempt: 20, want: 5 * time.Second},
	}

	for _, tt := range tests {
		if got := gatewayConnectRetryDelay(tt.attempt); got != tt.want {
			t.Fatalf("delay attempt %d = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestGatewayConnectRetryAttemptsAllowsServiceStartupWindow(t *testing.T) {
	if gatewayConnectRetryAttempts < 10 {
		t.Fatalf("gatewayConnectRetryAttempts = %d, want at least 10", gatewayConnectRetryAttempts)
	}
}

func TestGRPCServiceExcludedFromAutoRoutes(t *testing.T) {
	config := &Config{GRPCServiceExcludes: []string{
		"payment.provider.v1.PaymentProviderService",
		"game.provider.v1.GameProviderService",
	}}

	for _, service := range config.GRPCServiceExcludes {
		if !grpcServiceExcluded(config, service) {
			t.Fatalf("service %s should be excluded", service)
		}
	}
	if grpcServiceExcluded(config, "api.v1.BillingService") {
		t.Fatal("api.v1.BillingService should remain public")
	}
}

func TestAppendHTTPHeadersToMetadataIncludesCustomHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Add("X-Sign", "sig-1")
	headers.Add("X-Sign", "sig-2")
	headers.Set("X-App-Name", "web")
	headers.Set("Authorization", "Bearer token")
	headers.Set("Connection", "close")
	headers.Set("Content-Length", "10")
	headers.Set("Content-Type", "application/json")

	md := metadata.New(nil)
	appendHTTPHeadersToMetadata(md, headers)

	for key, want := range map[string][]string{
		"x-sign":        {"sig-1", "sig-2"},
		"x-app-name":    {"web"},
		"authorization": {"Bearer token"},
	} {
		if got := md.Get(key); !reflect.DeepEqual(got, want) {
			t.Fatalf("metadata %s = %#v, want %#v", key, got, want)
		}
	}
	for _, key := range []string{"connection", "content-length", "content-type"} {
		if got := md.Get(key); len(got) > 0 {
			t.Fatalf("metadata %s = %#v, want omitted transport header", key, got)
		}
	}
}

func TestBuildGRPCRequestBodyIncludesRawBodyAndHeaders(t *testing.T) {
	rawBody := []byte(`{"merchant_oid":"r1","status":"SUCCESS"}`)
	headers := http.Header{}
	headers.Set("Signature", "merchant,signature")
	headers.Add("X-Sign", "sig-1")
	headers.Add("X-Sign", "sig-2")

	got := buildGRPCRequestBody(
		map[string]string{"direct": "collect"},
		map[string][]string{"debug": {"1"}},
	)
	appendGatewayRequestMetadata(got, headers, rawBody)

	if got["direct"] != "collect" {
		t.Fatalf("direct = %v, want collect", got["direct"])
	}
	if got["debug"] != "1" {
		t.Fatalf("debug = %v, want 1", got["debug"])
	}
	if got["raw_body"] != base64.StdEncoding.EncodeToString(rawBody) {
		t.Fatalf("raw_body = %v, want base64 raw body", got["raw_body"])
	}
	gotHeaders, ok := got["headers"].(map[string]string)
	if !ok {
		t.Fatalf("headers = %#v, want map[string]string", got["headers"])
	}
	if gotHeaders["signature"] != "merchant,signature" {
		t.Fatalf("signature header = %q, want merchant,signature", gotHeaders["signature"])
	}
	if gotHeaders["x-sign"] != "sig-1,sig-2" {
		t.Fatalf("x-sign header = %q, want sig-1,sig-2", gotHeaders["x-sign"])
	}
}

func TestAppendGatewayRequestMetadataOverridesBodyFields(t *testing.T) {
	rawBody := []byte(`{"status":"SUCCESS"}`)
	reqBody := core.Map{
		"raw_body": "spoofed",
		"headers":  map[string]string{"signature": "spoofed"},
	}
	headers := http.Header{"Signature": {"real"}}

	appendGatewayRequestMetadata(reqBody, headers, rawBody)

	if reqBody["raw_body"] != base64.StdEncoding.EncodeToString(rawBody) {
		t.Fatalf("raw_body = %v, want gateway raw body", reqBody["raw_body"])
	}
	gotHeaders := reqBody["headers"].(map[string]string)
	if gotHeaders["signature"] != "real" {
		t.Fatalf("signature header = %q, want real", gotHeaders["signature"])
	}
}

func TestGRPCErrorToResponseHidesErrorDetails(t *testing.T) {
	got := grpcErrorToResponse(status.Error(codes.InvalidArgument, "parse request failed: invalid amount"))

	if got["msg"] != "internal server error" {
		t.Fatalf("msg = %v, want internal server error", got["msg"])
	}
	if got["code"] != int(codes.InvalidArgument) {
		t.Fatalf("code = %v, want %d", got["code"], codes.InvalidArgument)
	}
}
