//go:build !windows && !aix
// +build !windows,!aix

// Package reuseport provides TCP net.Listener with SO_REUSEPORT support.
//
// SO_REUSEPORT allows linear scaling server performance on multi-CPU servers.
// See https://www.nginx.com/blog/socket-sharding-nginx-release-1-9-1/ for more details :)
//
// The package is based on https://github.com/kavu/go_reuseport .
package reuseport

import (
	"net"
	"strings"

	"github.com/xs23933/core/v2/tcplisten"
)

func Listen(network, addr string) (net.Listener, error) {
	ln, err := cfg.NewListener(network, addr)

	if err != nil && strings.Contains(err.Error(), "SO_REUSEPORT") {
		return nil, &ErrNoReusePort{err}
	}
	return ln, err
}

var cfg = &tcplisten.Config{
	ReusePort:   true,
	DeferAccept: true,
	FastOpen:    true,
}
