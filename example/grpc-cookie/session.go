// Package grpccookie provides copyable response scaffolding for gRPC auth
// handlers. Authentication and session creation happen in the business service
// before calling LoginResponse. These functions use the current server RPC ctx.
package grpccookie

import (
	"context"
	"time"

	"github.com/xs23933/core/v3"
	"google.golang.org/protobuf/types/known/emptypb"
)

// LoginResponse writes an already-issued token to a cookie rather than JSON.
// Empty path uses "/"; SameSite=Lax and configured domain follow BaseCtx.
// Use HTTPS for Secure cookies, and replace Empty with your own response DTO.
func LoginResponse(ctx context.Context, token string, expires time.Time) (*emptypb.Empty, error) {
	if err := core.GrpcSetHeader(ctx, "Cache-Control", "no-store"); err != nil {
		return nil, err
	}
	if err := core.GrpcSetCookie(ctx, "session", token, expires, "", "httponly", true); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// LogoutResponse clears the cookie after the business service revokes the
// session. Match the Path and Domain used by LoginResponse.
func LogoutResponse(ctx context.Context) (*emptypb.Empty, error) {
	if err := core.GrpcSetHeader(ctx, "Cache-Control", "no-store"); err != nil {
		return nil, err
	}
	if err := core.GrpcRemoveCookie(ctx, "session", "/"); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
