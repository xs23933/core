package gateway

import (
	"net/http"

	"github.com/xs23933/core/v3/internal/grpchttp"
	"google.golang.org/grpc/metadata"
)

// Apply only explicitly declared initial response metadata. Never expose
// ordinary metadata, transport headers or trailers as HTTP response headers.
func appendGRPCResponseHeaders(headers http.Header, md metadata.MD) {
	for key, values := range md {
		name, ok := grpchttp.HeaderName(key)
		if !ok {
			continue
		}
		for _, value := range values {
			if grpchttp.ValidHeaderValue(value) {
				headers.Add(name, value)
			}
		}
	}
}
