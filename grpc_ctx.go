package core

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"google.golang.org/grpc/metadata"
)

// GRPCCtx is a request-scoped adapter for unary or stream server contexts.
// It does not change protobuf method signatures or own the context lifetime.
// Services should continue to accept context.Context, obtained with StdContext.
type GRPCCtx struct {
	ctx context.Context
}

// NewGRPCCtx wraps the current RPC context (stream.Context for streams).
// A nil context has background semantics; response writes still require a server RPC.
func NewGRPCCtx(ctx context.Context) *GRPCCtx { return &GRPCCtx{ctx: ctx} }

// StdContext preserves the original context, including deadline, cancellation,
// transport identity and Core's Gateway trust configuration.
func (c *GRPCCtx) StdContext() context.Context {
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// GetHeader reads the first incoming metadata value. It is not an identity check.
func (c *GRPCCtx) GetHeader(key string) string { return GrpcHeader(c.StdContext(), key) }

// HeaderValues reads all incoming values and returns an independent copy.
func (c *GRPCCtx) HeaderValues(key string) []string {
	md, _ := metadata.FromIncomingContext(c.StdContext())
	return append([]string(nil), md.Get(key)...)
}

// RequestMetadata returns an independent copy of incoming metadata.
// Editing it affects neither the request nor a response/downstream call.
func (c *GRPCCtx) RequestMetadata() metadata.MD {
	md, _ := metadata.FromIncomingContext(c.StdContext())
	return md.Copy()
}

// ClientIP uses ExtractClientInfo's Gateway trust checks, never a raw IP header.
func (c *GRPCCtx) ClientIP() string { return ExtractClientInfo(c.StdContext()).IP }

// Cookie reads and URL-decodes an incoming cookie, like BaseCtx.Cookies.
// Missing cookies return http.ErrNoCookie; malformed URL escapes return an error.
func (c *GRPCCtx) Cookie(name string) (string, error) {
	req := http.Request{Header: http.Header{"Cookie": c.HeaderValues("cookie")}}
	cookie, err := req.Cookie(name)
	if err != nil {
		return "", err
	}
	return url.QueryUnescape(cookie.Value)
}

// SetHeader queues HTTP response headers through Core's Gateway bridge.
// Repeated calls append values; this does not modify request or outgoing metadata.
func (c *GRPCCtx) SetHeader(key string, values ...string) error {
	return GrpcSetHeader(c.StdContext(), key, values...)
}

// SetCookie queues a response cookie using the existing BaseCtx-compatible defaults.
func (c *GRPCCtx) SetCookie(name, value string, exp time.Time, path string, args ...any) error {
	return GrpcSetCookie(c.StdContext(), name, value, exp, path, args...)
}

// WriteCookie queues a complete response cookie without adding defaults or escaping.
func (c *GRPCCtx) WriteCookie(cookie *http.Cookie) error { return GrpcCookie(c.StdContext(), cookie) }

// RemoveCookie queues an expired response cookie using the existing removal rules.
func (c *GRPCCtx) RemoveCookie(name, path string, domain ...string) error {
	return GrpcRemoveCookie(c.StdContext(), name, path, domain...)
}

// OutgoingContext returns a derived context for a downstream RPC. Only explicitly
// supplied metadata is added; existing outgoing keys are replaced by supplied keys.
// Incoming metadata is never automatically forwarded. Neither source map is mutated
// or aliased. The original deadline/cancellation and other context values are retained.
func (c *GRPCCtx) OutgoingContext(md metadata.MD) context.Context {
	ctx := c.StdContext()
	existing, _ := metadata.FromOutgoingContext(ctx)
	outgoing := existing.Copy()
	if outgoing == nil {
		outgoing = metadata.MD{}
	}
	for key, values := range md {
		outgoing.Set(key, append([]string(nil), values...)...)
	}
	return metadata.NewOutgoingContext(ctx, outgoing)
}
