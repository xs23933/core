package gateway

import (
	"strings"
	"testing"

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

func testPtr[T any](v T) *T {
	return &v
}
