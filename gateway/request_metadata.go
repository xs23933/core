package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc/metadata"
)

// requestMetadata separates raw transport headers from middleware identity claims.
// The core-identity- namespace is always reserved. Additional legacy names can
// be protected explicitly so existing custom header contracts stay valid.
func (gw *EtcdGateway) requestMetadata(ctx core.Ctx) metadata.MD {
	var keys []string
	if gw.config != nil {
		keys = gw.config.ProtectedMetadataKeys
	}
	md := gatewayRequestMetadata(ctx.Request().Header, ctx.Vars(), keys)
	// Client IP is always reserved and cannot be overridden even by Vars.
	md.Delete(core.GatewayClientIPMetadata)
	if ip := ctx.RemoteIP(); ip != nil {
		md.Set(core.GatewayClientIPMetadata, ip.String())
	}
	return md
}

func gatewayRequestMetadata(headers http.Header, vars core.Map, protected []string) metadata.MD {
	md := metadata.New(nil)
	appendHTTPHeadersToMetadata(md, headers)
	for key := range md {
		if strings.HasPrefix(key, "core-identity-") {
			md.Delete(key)
		}
	}
	for _, key := range protected {
		md.Delete(strings.ToLower(strings.TrimSpace(key)))
	}
	for key, value := range vars {
		key = strings.ToLower(key)
		if grpcMetadataHeaderSkipped(key) {
			continue
		}
		md.Set(key, fmt.Sprint(value))
	}
	return md
}
