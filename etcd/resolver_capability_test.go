package etcd

import (
	"reflect"
	"sort"
	"testing"

	"google.golang.org/grpc/resolver"
)

type capabilityClientConn struct {
	resolver.ClientConn
	state resolver.State
}

func (c *capabilityClientConn) UpdateState(state resolver.State) error { c.state = state; return nil }

// TestResolverSkipsHTTPOnly 保留旧注册格式，同时排除显式 HTTP-only 和未就绪实例。
func TestResolverSkipsHTTPOnly(t *testing.T) {
	notReady := false
	discovery := &Discovery{}
	discovery.storeServices(map[string]map[string]*ServiceInfo{"demo": {
		"legacy":  {Addr: "legacy"},
		"grpc":    {Addr: "grpc", Metadata: map[string]string{MetadataGRPCEnabled: "true"}},
		"http":    {Addr: "http", Metadata: map[string]string{MetadataGRPCEnabled: "false"}},
		"pending": {Addr: "pending", Ready: &notReady},
	}})
	connection := &capabilityClientConn{}
	(&etcdResolver{cc: connection, discovery: discovery, serviceName: "demo"}).resolve()
	addresses := []string{}
	for _, address := range connection.state.Addresses {
		addresses = append(addresses, address.Addr)
	}
	sort.Strings(addresses)
	if !reflect.DeepEqual(addresses, []string{"grpc", "legacy"}) {
		t.Fatalf("resolved addresses=%v", addresses)
	}
}
