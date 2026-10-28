package gateway

import (
	"context"
	"testing"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	interop "google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/status"
)

type receiveLimitServer struct {
	interop.UnimplementedTestServiceServer
}

func (receiveLimitServer) UnaryCall(context.Context, *interop.SimpleRequest) (*interop.SimpleResponse, error) {
	return &interop.SimpleResponse{Payload: &interop.Payload{Body: make([]byte, 5<<20)}}, nil
}
func TestReflectionProxyConfiguredReceiveLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  codes.Code
	}{
		{"default", 0, codes.ResourceExhausted}, {"configured", 8 << 20, codes.OK}, {"bounded", 1 << 20, codes.ResourceExhausted}, {"negative", -1, codes.ResourceExhausted}, {"above_max", 65 << 20, codes.ResourceExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := grpc.NewServer()
			interop.RegisterTestServiceServer(server, receiveLimitServer{})
			proxy := metadataTestProxy(t, server)
			proxy.app = core.New(core.Options{"grpc": core.Options{"max_recv_message_bytes": tc.limit}})
			_, err := proxy.Invoke(context.Background(), interop.TestService_UnaryCall_FullMethodName, []byte(`{}`))
			if status.Code(err) != tc.want {
				t.Fatalf("代理传输结果=%v，期望%s", err, tc.want)
			}
		})
	}
}
