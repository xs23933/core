package gateway

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestProtoJSONAnyUsesDynamicResolver(t *testing.T) {
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(anypb.File_google_protobuf_any_proto),
			{
				Name:       testPtr("test_any.proto"),
				Package:    testPtr("test"),
				Syntax:     testPtr("proto3"),
				Dependency: []string{"google/protobuf/any.proto"},
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: testPtr("Inner"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     testPtr("name"),
								Number:   testPtr[int32](1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
								JsonName: testPtr("name"),
							},
						},
					},
					{
						Name: testPtr("Wrapper"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     testPtr("payload"),
								Number:   testPtr[int32](1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
								TypeName: testPtr(".google.protobuf.Any"),
								JsonName: testPtr("payload"),
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build files: %v", err)
	}

	desc, err := files.FindDescriptorByName("test.Wrapper")
	if err != nil {
		t.Fatalf("find wrapper: %v", err)
	}
	msg := dynamicpb.NewMessage(desc.(protoreflect.MessageDescriptor))
	resolver := dynamicpb.NewTypes(files)

	err = protoJSONUnmarshal([]byte(`{"payload":{"@type":"type.googleapis.com/test.Inner","name":"alice"}}`), msg, resolver)
	if err != nil {
		t.Fatalf("unmarshal any json: %v", err)
	}

	out, err := protoJSONMarshal(msg, resolver)
	if err != nil {
		t.Fatalf("marshal any json: %v", err)
	}
	if !strings.Contains(string(out), `"@type":"type.googleapis.com/test.Inner"`) {
		t.Fatalf("marshal output missing any type: %s", string(out))
	}
	if !strings.Contains(string(out), `"name":"alice"`) {
		t.Fatalf("marshal output missing nested data: %s", string(out))
	}
}

func TestProtoJSONUnmarshalPromotesFlatPagination(t *testing.T) {
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    testPtr("pagination.proto"),
				Package: testPtr("api.v1"),
				Syntax:  testPtr("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: testPtr("PageRequest"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: testPtr("page"), JsonName: testPtr("page"), Number: testPtr[int32](1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
							{Name: testPtr("limit"), JsonName: testPtr("limit"), Number: testPtr[int32](2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
						},
					},
					{
						Name: testPtr("TransactionListRequest"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: testPtr("user_id"), JsonName: testPtr("userId"), Number: testPtr[int32](1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
							{Name: testPtr("page"), JsonName: testPtr("page"), Number: testPtr[int32](2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: testPtr(".api.v1.PageRequest")},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build files: %v", err)
	}

	desc, err := files.FindDescriptorByName("api.v1.TransactionListRequest")
	if err != nil {
		t.Fatalf("find request: %v", err)
	}
	msg := dynamicpb.NewMessage(desc.(protoreflect.MessageDescriptor))

	err = protoJSONUnmarshal([]byte(`{"user_id":"27333218910867456","page":"1","limit":"10"}`), msg, dynamicpb.NewTypes(files))
	if err != nil {
		t.Fatalf("unmarshal flat pagination: %v", err)
	}

	pageField := msg.Descriptor().Fields().ByName("page")
	page := msg.Get(pageField).Message()
	if got := page.Get(page.Descriptor().Fields().ByName("page")).Int(); got != 1 {
		t.Fatalf("page = %d, want 1", got)
	}
	if got := page.Get(page.Descriptor().Fields().ByName("limit")).Int(); got != 10 {
		t.Fatalf("limit = %d, want 10", got)
	}
	if got := msg.Get(msg.Descriptor().Fields().ByName("user_id")).String(); got != "27333218910867456" {
		t.Fatalf("user_id = %q, want 27333218910867456", got)
	}
}

func TestReflectionProxyInvokeReturnsInvalidArgumentForParseFailure(t *testing.T) {
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    testPtr("parse_failure.proto"),
				Package: testPtr("test"),
				Syntax:  testPtr("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: testPtr("Request"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: testPtr("amount"), JsonName: testPtr("amount"), Number: testPtr[int32](1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE.Enum()},
						},
					},
					{Name: testPtr("Response")},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build files: %v", err)
	}
	reqDesc, err := files.FindDescriptorByName("test.Request")
	if err != nil {
		t.Fatalf("find request: %v", err)
	}
	respDesc, err := files.FindDescriptorByName("test.Response")
	if err != nil {
		t.Fatalf("find response: %v", err)
	}
	resolver := dynamicpb.NewTypes(files)

	proxy := &ReflectionProxy{}
	proxy.methodCache.Store("/test.Service/Post", &MethodDescriptor{
		FullMethod: "/test.Service/Post",
		NewRequest: func() protoreflect.ProtoMessage {
			return dynamicpb.NewMessage(reqDesc.(protoreflect.MessageDescriptor))
		},
		NewResponse: func() protoreflect.ProtoMessage {
			return dynamicpb.NewMessage(respDesc.(protoreflect.MessageDescriptor))
		},
		Resolver: resolver,
	})

	_, err = proxy.Invoke(context.Background(), "/test.Service/Post", []byte(`{"amount":"bad"}`))
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Invoke error = %v, want grpc status", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("Invoke code = %s, want %s", st.Code(), codes.InvalidArgument)
	}
	if !strings.Contains(st.Message(), "parse request failed") {
		t.Fatalf("Invoke message = %q, want parse request failed", st.Message())
	}
}

func testPtr[T any](v T) *T {
	return &v
}
