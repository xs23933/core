package gateway

import (
	"testing"
	"time"
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
