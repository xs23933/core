package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/xs23933/core/v3/internal/grpchttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// GrpcSetHeader queues HTTP response header values in gRPC initial metadata.
// Repeated calls append values, as with grpc.SetHeader. Gateway converts only
// this explicit core-http-<lowercase-name>-bin contract, including on RPC errors.
// Transport headers and invalid names/values are rejected before queuing.
// Names must also fit the gRPC metadata alphabet (letters, digits, -_.).
// Call before sending initial headers; ctx must be the current server RPC context.
func GrpcSetHeader(ctx context.Context, key string, values ...string) error {
	if ctx == nil {
		return errors.New("core: gRPC response header requires a server context")
	}
	if !grpchttp.ValidHeaderName(key) {
		return fmt.Errorf("core: invalid, reserved or unsupported HTTP response header %q", key)
	}
	if len(values) == 0 {
		return errors.New("core: HTTP response header requires at least one value")
	}
	for _, value := range values {
		if !grpchttp.ValidHeaderValue(value) {
			return errors.New("core: invalid HTTP response header value")
		}
	}
	md := metadata.MD{}
	md.Set(grpchttp.HeaderKey(key), values...)
	return grpc.SetHeader(ctx, md)
}

// GrpcSetCookie mirrors BaseCtx.SetCookie: empty path becomes "/", value is
// URL-escaped, SameSite defaults to Lax, a case-insensitive "httponly" string
// enables HttpOnly, other strings set Domain, and bools set Secure. Empty Domain
// falls back to the current Core server's domain config. Unknown args are ignored.
// Use GrpcCookie for explicit SameSite, MaxAge and a host-only cookie.
func GrpcSetCookie(ctx context.Context, name, value string, exp time.Time, path string, args ...any) error {
	return GrpcCookie(ctx, newCookie(name, value, exp, path, grpcCookieDomain(ctx), args...))
}

// GrpcRemoveCookie mirrors BaseCtx.RemoveCookie, including its unchanged path
// and optional Domain (the first value wins). Use the original Path/Domain.
func GrpcRemoveCookie(ctx context.Context, name, path string, dom ...string) error {
	return GrpcCookie(ctx, removedCookie(name, path, grpcCookieDomain(ctx), dom...))
}

// GrpcCookie queues a complete cookie without applying defaults or escaping its
// value, like BaseCtx.Cookie. Invalid cookies return an error instead of being
// silently dropped or sanitized. The caller-owned cookie is not modified.
func GrpcCookie(ctx context.Context, cookie *http.Cookie) error {
	if cookie == nil {
		return errors.New("core: nil gRPC response cookie")
	}
	if err := cookie.Valid(); err != nil {
		return fmt.Errorf("core: invalid gRPC response cookie: %w", err)
	}
	return GrpcSetHeader(ctx, "Set-Cookie", cookie.String())
}

type grpcCoreContextKey struct{}

func grpcCookieDomain(ctx context.Context) string {
	if ctx != nil {
		if app, ok := ctx.Value(grpcCoreContextKey{}).(*Core); ok {
			return app.Conf.GetString("domain")
		}
	}
	return ""
}

func (app *Core) grpcResponseContext(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return handler(context.WithValue(ctx, grpcCoreContextKey{}, app), req)
}

type grpcResponseStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *grpcResponseStream) Context() context.Context { return s.ctx }

func (app *Core) grpcStreamResponseContext(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(srv, &grpcResponseStream{ServerStream: stream, ctx: context.WithValue(stream.Context(), grpcCoreContextKey{}, app)})
}
