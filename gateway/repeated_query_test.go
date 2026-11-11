package gateway

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/xs23933/core/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func repeatedQueryDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("repeated_query.proto"), Package: proto.String("test"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Query"), Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("wallet_ids"), JsonName: proto.String("walletIds"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()},
			{Name: proto.String("counts"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()},
			{Name: proto.String("enabled"), Number: proto.Int32(3), Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()},
			{Name: proto.String("limit"), Number: proto.Int32(4), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()},
		}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return file.Messages().Get(0)
}

func TestGRPCRepeatedQueryUsesDescriptorAndPreservesIdentity(t *testing.T) {
	desc := repeatedQueryDescriptor(t)
	for _, tc := range []struct {
		name  string
		query map[string][]string
		body  core.Map
		want  []string
	}{
		{name: "多个原名", query: map[string][]string{"wallet_ids": {"78990469979176960", "0007"}, "limit": {"20", "30"}}, want: []string{"78990469979176960", "0007"}},
		{name: "单个JSON别名", query: map[string][]string{"walletIds": {"78990469979176960"}}, want: []string{"78990469979176960"}},
		{name: "正文覆盖查询", query: map[string][]string{"wallet_ids": {"spoofed"}}, body: core.Map{"wallet_ids": []string{"body"}}, want: []string{"body"}},
		{name: "空查询", query: map[string][]string{"wallet_ids": {}}, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := mergeGRPCRequestBody(nil, tc.query, tc.body, desc)
			data, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			message := dynamicpb.NewMessage(desc)
			if err = protoJSONUnmarshal(data, message, nil); err != nil {
				t.Fatal(err)
			}
			list := message.Get(desc.Fields().ByName("wallet_ids")).List()
			got := make([]string, list.Len())
			for i := range got {
				got[i] = list.Get(i).String()
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("身份或顺序变化: %v", got)
			}
			if len(tc.query["limit"]) > 0 && message.Get(desc.Fields().ByName("limit")).Int() != 20 {
				t.Fatal("标量查询不再保留原先首值语义")
			}
		})
	}
}

func TestGRPCRepeatedQueryScalarTypesAndBodyValidation(t *testing.T) {
	desc := repeatedQueryDescriptor(t)
	values := mergeGRPCRequestBody(nil, map[string][]string{"counts": {"9007199254740993", "2"}, "enabled": {"true", "false"}}, nil, desc)
	data, _ := json.Marshal(values)
	message := dynamicpb.NewMessage(desc)
	if err := protoJSONUnmarshal(data, message, nil); err != nil {
		t.Fatal(err)
	}
	if message.Get(desc.Fields().ByName("counts")).List().Get(0).Int() != 9007199254740993 {
		t.Fatal("整数精度丢失")
	}
	if !message.Get(desc.Fields().ByName("enabled")).List().Get(0).Bool() || message.Get(desc.Fields().ByName("enabled")).List().Get(1).Bool() {
		t.Fatal("布尔查询错误")
	}
	for _, body := range []core.Map{{"wallet_ids": "not-an-array"}, {"enabled": []string{"not-a-bool"}}} {
		data, _ := json.Marshal(mergeGRPCRequestBody(nil, nil, body, desc))
		if err := protoJSONUnmarshal(data, dynamicpb.NewMessage(desc), nil); err == nil {
			t.Fatal("错误正文被查询转换放宽")
		}
	}
}
