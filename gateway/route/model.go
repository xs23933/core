package route

import "time"

// Protocol identifies the upstream protocol used by a route definition.
type Protocol string

const (
	ProtocolGRPC Protocol = "grpc"
	ProtocolHTTP Protocol = "http"
)

// Source identifies how a route was produced.
type Source string

const (
	SourceManual   Source = "manual"
	SourceAutoHTTP Source = "auto_http"
	SourceAutoGRPC Source = "auto_grpc"
)

// Definition is the shared wire contract for a Gateway route.
type Definition struct {
	ID           string            `json:"id" yaml:"id"`
	Slot         string            `json:"slot,omitempty" yaml:"-"`
	Source       Source            `json:"source,omitempty" yaml:"source,omitempty"`
	Owner        string            `json:"owner,omitempty" yaml:"owner,omitempty"`
	Protocol     Protocol          `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	Method       string            `json:"method" yaml:"method"`
	Path         string            `json:"path" yaml:"path"`
	ServiceName  string            `json:"service_name" yaml:"service_name"`
	GRPCMethod   string            `json:"grpc_method,omitempty" yaml:"grpc_method,omitempty"`
	UpstreamPath string            `json:"upstream_path,omitempty" yaml:"upstream_path,omitempty"`
	Description  string            `json:"description,omitempty" yaml:"description,omitempty"`
	Headers      map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Enabled      bool              `json:"enabled" yaml:"enabled"`
	CreatedAt    time.Time         `json:"created_at" yaml:"-"`
	UpdatedAt    time.Time         `json:"updated_at" yaml:"-"`
}

// Catalog is the complete automatic HTTP route set for one logical service.
type Catalog struct {
	ServiceName string        `json:"service_name"`
	Routes      []*Definition `json:"routes"`
}
