package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoregistry"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func exactTestDescriptor(t *testing.T) *MethodDescriptor {
	t.Helper()
	field := func(name string, n int32, kind descriptorpb.FieldDescriptorProto_Type, typeName string, repeated bool) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(n), Type: kind.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		if repeated {
			f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}
		return f
	}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{Name: proto.String("exact_test.proto"), Package: proto.String("test.exact"), Syntax: proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{
		{Name: proto.String("Request"), Field: []*descriptorpb.FieldDescriptorProto{field("raw_body", 1, descriptorpb.FieldDescriptorProto_TYPE_BYTES, "", false)}},
		{Name: proto.String("Row"), Field: []*descriptorpb.FieldDescriptorProto{field("bet", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, "", false), field("insert_time", 2, descriptorpb.FieldDescriptorProto_TYPE_INT64, "", false), field("id", 3, descriptorpb.FieldDescriptorProto_TYPE_STRING, "", false)}},
		{Name: proto.String("Data"), Field: []*descriptorpb.FieldDescriptorProto{field("Amount", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, "", false), field("List", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".test.exact.Row", true)}},
		{Name: proto.String("Response"), Field: []*descriptorpb.FieldDescriptorProto{field("code", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32, "", false), field("data", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".test.exact.Data", false)}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &MethodDescriptor{FullMethod: "/test.exact.Service/Post", NewRequest: func() proto.Message { return dynamicpb.NewMessage(fd.Messages().ByName("Request")) }, NewResponse: func() proto.Message { return dynamicpb.NewMessage(fd.Messages().ByName("Response")) }}
}

func exactTestBinding() ExactJSONBinding {
	return ExactJSONBinding{ServiceName: "apis", FullMethod: "/test.exact.Service/Post", RequestMessage: "test.exact.Request", ResponseMessage: "test.exact.Response", MoneyPaths: []string{"data.Amount", "data.List[*].bet"}, IntegerPaths: []string{"data.List[*].insert_time"}, MaxRecvBytes: 32 << 20}
}

func TestExactJSONNumbersFailClosed(t *testing.T) {
	b := exactTestBinding()
	desc := exactTestDescriptor(t)
	if err := b.validateDescriptor(desc); err != nil {
		t.Fatal(err)
	}
	for _, amount := range []string{"0", "0.00000001", "90071992.54740991", "-90071992.54740991", "92233720368.54775807", "-92233720368.54775808"} {
		t.Run(amount, func(t *testing.T) {
			msg := desc.NewResponse()
			input := `{"data":{"Amount":"` + amount + `","List":[{"bet":"0.00000001","insert_time":"9007199254740993","id":"9007199254740993"}]}}`
			if err := protoJSONUnmarshal([]byte(input), msg, nil); err != nil {
				t.Fatal(err)
			}
			out, err := b.marshalResponse(msg, desc.Resolver)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			compact := strings.ReplaceAll(string(out), " ", "")
			for _, want := range []string{`"code":0`, `"Amount":` + amount, `"bet":0.00000001`, `"insert_time":9007199254740993`, `"id":"9007199254740993"`} {
				if !strings.Contains(compact, want) {
					t.Fatalf("missing %s: %s", want, out)
				}
			}
		})
	}
	for _, input := range []string{`{}`, `{"data":{}}`, `{"data":{"Amount":""}}`, `{"data":{"Amount":"1e-8"}}`, `{"data":{"Amount":"0.000000001"}}`, `{"data":{"Amount":"92233720368.54775808"}}`, `{"data":{"Amount":"-92233720368.54775809"}}`} {
		msg := desc.NewResponse()
		if err := protoJSONUnmarshal([]byte(input), msg, nil); err != nil {
			t.Fatal(err)
		}
		if out, err := b.marshalResponse(msg, nil); err == nil {
			t.Fatalf("accepted %s => %s", input, out)
		}
	}
	msg := desc.NewResponse()
	if err := protoJSONUnmarshal([]byte(`{"code":9}`), msg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.marshalResponse(msg, nil); err != nil {
		t.Fatalf("error response: %v", err)
	}
}

func TestExactJSONErrorDataStillRequiresExactValues(t *testing.T) {
	b := exactTestBinding()
	msg := exactTestDescriptor(t).NewResponse()
	if err := protoJSONUnmarshal([]byte(`{"code":9,"data":{"Amount":"bad"}}`), msg, nil); err != nil {
		t.Fatal(err)
	}
	if out, err := b.marshalResponse(msg, nil); err == nil {
		t.Fatalf("malformed present error data accepted: %s", out)
	}
}

func TestExactJSONRegistrationAndSchema(t *testing.T) {
	gw := &EtcdGateway{}
	b := exactTestBinding()
	if err := gw.RegisterExactJSONBinding(b); err != nil {
		t.Fatal(err)
	}
	if err := gw.RegisterExactJSONBinding(b); err == nil {
		t.Fatal("duplicate accepted")
	}
	b.MoneyPaths[0] = "data.missing"
	if got := gw.exactJSONBinding("apis", b.FullMethod); got.MoneyPaths[0] != "data.Amount" {
		t.Fatal("registration retained caller slice")
	}
	bad := exactTestBinding()
	bad.ResponseMessage = "test.exact.Changed"
	if err := bad.validateDescriptor(exactTestDescriptor(t)); err == nil {
		t.Fatal("changed response accepted")
	}
	bad = exactTestBinding()
	bad.MoneyPaths = []string{"data.List.bet"}
	if err := bad.validateDescriptor(exactTestDescriptor(t)); err == nil {
		t.Fatal("array mismatch accepted")
	}
	bad = exactTestBinding()
	bad.MoneyPaths = []string{"data.List[*].insert_time"}
	if err := bad.validateDescriptor(exactTestDescriptor(t)); err == nil {
		t.Fatal("numeric money schema accepted")
	}
}

func TestExactJSONRealReflectionHTTP(t *testing.T) {
	base := exactTestDescriptor(t)
	fileProto := protodesc.ToFileDescriptorProto(base.NewRequest().ProtoReflect().Descriptor().ParentFile())
	fileProto.Service = []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("Service"), Method: []*descriptorpb.MethodDescriptorProto{{Name: proto.String("Post"), InputType: proto.String(".test.exact.Request"), OutputType: proto.String(".test.exact.Response")}}}}
	fd, err := protodesc.NewFile(fileProto, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := new(protoregistry.Files)
	if err := files.RegisterFile(fd); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var received atomic.Value
	srv := grpc.NewServer(grpc.MaxSendMsgSize(32 << 20))
	srv.RegisterService(&grpc.ServiceDesc{ServiceName: "test.exact.Service", HandlerType: (*interface{})(nil), Metadata: "exact_test.proto", Methods: []grpc.MethodDesc{{MethodName: "Post", Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		req := dynamicpb.NewMessage(fd.Messages().ByName("Request"))
		if err := dec(req); err != nil {
			return nil, err
		}
		body := req.Get(req.Descriptor().Fields().ByName("raw_body")).Bytes()
		received.Store(string(body))
		calls.Add(1)
		if err := core.GrpcSetHeader(ctx, "X-Exact-Test", "preserved"); err != nil {
			return nil, err
		}
		resp := dynamicpb.NewMessage(fd.Messages().ByName("Response"))
		row := `{"bet":"0.00000001","insert_time":"9007199254740993","id":"9007199254740993"}`
		rows := row
		if strings.Contains(string(body), "large") {
			row = `{"bet":"0.00000001","insert_time":"9007199254740993","id":"` + strings.Repeat("x", 450) + `"}`
			rows = strings.TrimSuffix(strings.Repeat(row+",", 10000), ",")
		}
		err := protoJSONUnmarshal([]byte(`{"data":{"Amount":"90071992.54740991","List":[`+rows+`]}}`), resp, nil)
		return resp, err
	}}}}, struct{}{})
	reflectionpb.RegisterServerReflectionServer(srv, reflection.NewServer(reflection.ServerOptions{Services: srv, DescriptorResolver: files}))
	listener := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { srv.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///exact", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proxy, err := newReflectionProxyFromConnContext(ctx, core.New(), "exact", conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := core.New()
	gw := &EtcdGateway{app: app, connPool: NewConnectionPool()}
	binding := exactTestBinding()
	for _, service := range []string{"apis", "ordinary", "drift", "small"} {
		pool := gw.connPool.GetOrCreate(app, service)
		pool.storeState(servicePoolState{byID: map[string]*ReflectionProxy{"one": proxy}, all: []*ReflectionProxy{proxy}, methods: map[string][]*ReflectionProxy{binding.FullMethod: {proxy}}})
		if service != "ordinary" {
			b := binding
			b.ServiceName = service
			if service == "drift" {
				b.ResponseMessage = "test.exact.Changed"
			}
			if service == "small" {
				b.MaxRecvBytes = 4 << 20
			}
			if err := gw.RegisterExactJSONBinding(b); err != nil {
				t.Fatal(err)
			}
		}
		gw.registerRoute(&Route{Method: http.MethodPost, Path: "/" + service, ServiceName: service, GRPCMethod: binding.FullMethod})
	}
	raw := " { \"Amount\": 9.007199254740991e7, \"raw_body\":\"forged\", \"headers\":{} } \n"
	for _, tt := range []struct {
		path, body string
		status     int
	}{{"/apis", raw, 200}, {"/ordinary", `{}`, 200}, {"/apis?raw_body=forged", raw, 400}, {"/drift", raw, 500}, {"/apis", `{"large":true}`, 200}, {"/small", `{"large":true}`, 429}} {
		before := calls.Load()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
		req.RemoteAddr = "203.0.113.10:5000"
		app.ServeHTTP(rec, req)
		if rec.Code != tt.status {
			t.Fatalf("%s status %d want %d: %s", tt.path, rec.Code, tt.status, rec.Body.String()[:min(300, rec.Body.Len())])
		}
		if tt.status == 400 || tt.status == 500 {
			if calls.Load() != before {
				t.Fatal("rejected request reached business RPC")
			}
			continue
		}
		if tt.status != 200 {
			continue
		}
		if got := received.Load().(string); got != tt.body {
			t.Fatalf("body changed: %q", got)
		}
		if rec.Header().Get("X-Exact-Test") != "preserved" {
			t.Fatal("response metadata lost")
		}
		result := strings.ReplaceAll(rec.Body.String(), " ", "")
		if tt.path == "/ordinary" {
			if !strings.Contains(result, `"Amount":"90071992.54740991"`) {
				t.Fatal("unbound response changed")
			}
		} else {
			if !strings.Contains(result, `"Amount":90071992.54740991`) {
				t.Fatal("exact response lost numeric token")
			}
		}
		if strings.Contains(tt.body, "large") && rec.Body.Len() < 4<<20 {
			t.Fatal("large response not exercised")
		}
	}
}

func TestExactJSONRejectsMalformedLeavesAndContainers(t *testing.T) {
	parts, err := parseExactPath("data.List[*].bet")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"data":null}`, `{"data":[]}`, `{"data":{"List":null}}`, `{"data":{"List":{}}}`, `{"data":{"List":[null]}}`, `{"data":{"List":[{}]}}`, `{"data":{"List":[{"bet":0}]}}`, `{"data":{"List":[{"bet":null}]}}`, `{"data":{"List":[{"bet":"NaN"}]}}`} {
		if out, err := convertExactPath([]byte(raw), parts, true); err == nil {
			t.Fatalf("accepted %s => %s", raw, out)
		}
	}
	for _, value := range []string{"9223372036854775808", "-9223372036854775809", "1.0", "+1", "01", "1e2", "", " 1"} {
		if out, err := exactNumberToken(value, false); err == nil {
			t.Fatalf("integer accepted %q => %s", value, out)
		}
	}
	for _, value := range []string{"9223372036854775807", "-9223372036854775808", "0"} {
		if _, err := exactNumberToken(value, false); err != nil {
			t.Fatalf("valid integer %q: %v", value, err)
		}
	}
}

func TestExactJSONRegistrationRejectsBadConfiguration(t *testing.T) {
	for _, change := range []func(*ExactJSONBinding){
		func(b *ExactJSONBinding) { b.ServiceName = "" }, func(b *ExactJSONBinding) { b.FullMethod = "bad" }, func(b *ExactJSONBinding) { b.RequestMessage = "bad" }, func(b *ExactJSONBinding) { b.MaxRecvBytes = 0 }, func(b *ExactJSONBinding) { b.MaxRecvBytes = 32<<20 + 1 }, func(b *ExactJSONBinding) { b.MoneyPaths = []string{"data.List[0].bet"} }, func(b *ExactJSONBinding) { b.MoneyPaths = []string{"outside"} }, func(b *ExactJSONBinding) { b.MoneyPaths = []string{"data.List[*]"} }, func(b *ExactJSONBinding) { b.IntegerPaths = []string{"data.Amount"} },
	} {
		b := exactTestBinding()
		change(&b)
		if err := (&EtcdGateway{}).RegisterExactJSONBinding(b); err == nil {
			t.Fatalf("invalid binding accepted: %+v", b)
		}
	}
}
