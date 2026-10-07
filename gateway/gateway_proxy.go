package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jhump/protoreflect/grpcreflect"
	"github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

type ReflectionProxy struct {
	generation string
	conn       *grpc.ClientConn
	schema     *ReflectionSchema
	addr       string
	app        *core.Core
	ready      func() bool
	closeFn    func() error
	closeOnce  sync.Once
	closeErr   error
}

// ReflectionSchema is an immutable reflected method set shared by connections
// only after their process protocols have been independently verified. The map is built completely before the
// schema is published to a ServicePool.
type ReflectionSchema struct {
	fingerprint string
	methods     map[string]*MethodDescriptor
}

type MethodDescriptor struct {
	Contract    string // deterministic fingerprint of reachable request/response types
	FullMethod  string
	Package     string // proto package, e.g. "v1.auth"
	Service     string // service name, e.g. "UserService"
	Method      string // method name, e.g. "PostLogin"
	NewRequest  func() proto.Message
	NewResponse func() proto.Message
	Resolver    interface {
		protoregistry.MessageTypeResolver
		protoregistry.ExtensionTypeResolver
	}
}

func NewReflectionProxy(app *core.Core, addr string) (*ReflectionProxy, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create client conn failed: %w", err)
	}
	return newReflectionProxyFromConnContext(context.Background(), app, addr, conn, nil)
}

func newReflectionProxyForService(app *core.Core, serviceName, addr string) (*ReflectionProxy, error) {
	return newReflectionProxyForServiceContext(context.Background(), app, serviceName, addr, nil)
}

func newReflectionProxyForServiceContext(ctx context.Context, app *core.Core, serviceName, addr string, schema *ReflectionSchema) (*ReflectionProxy, error) {
	conn, err := app.GrpcClientAt(serviceName, addr)
	if err != nil {
		return nil, fmt.Errorf("create client conn failed: %w", err)
	}
	return newReflectionProxyFromConnContext(ctx, app, addr, conn, schema)
}

func newReflectionProxyFromConn(app *core.Core, addr string, conn *grpc.ClientConn) (*ReflectionProxy, error) {
	return newReflectionProxyFromConnContext(context.Background(), app, addr, conn, nil)
}

func newReflectionProxyFromConnContext(ctx context.Context, app *core.Core, addr string, conn *grpc.ClientConn, schema *ReflectionSchema) (*ReflectionProxy, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if schema == nil {
		var err error
		schema, err = discoverReflectionSchema(ctx, conn)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("discover and register failed: %w", err)
		}
	}
	return &ReflectionProxy{conn: conn, schema: schema, addr: addr, app: app}, nil
}

func discoverReflectionSchema(parent context.Context, conn *grpc.ClientConn) (*ReflectionSchema, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	methods := make(map[string]*MethodDescriptor)
	if err := discoverReflectionMethods(ctx, conn, methods); err != nil {
		return nil, err
	}
	return &ReflectionSchema{methods: methods, fingerprint: reflectionFingerprint(methods)}, nil
}

func discoverReflectionMethods(ctx context.Context, conn *grpc.ClientConn, methodsByName map[string]*MethodDescriptor) error {
	if conn == nil {
		return errors.New("reflection connection is nil")
	}

	stub := grpc_reflection_v1alpha.NewServerReflectionClient(conn)
	refClient := grpcreflect.NewClientV1Alpha(ctx, stub)
	defer refClient.Reset()

	services, err := refClient.ListServices()
	if err != nil {
		return fmt.Errorf("list services failed: %w", err)
	}

	for _, serviceName := range services {
		if strings.Contains(serviceName, "grpc.reflection") {
			continue
		}

		svcDesc, err := refClient.ResolveService(serviceName)
		if err != nil {
			return fmt.Errorf("resolve service %s: %w", serviceName, err)
		}

		// 递归收集文件及其所有依赖（包括 google/protobuf/timestamp.proto 等 well-known types）
		seen := make(map[string]bool)
		fdSet := &descriptorpb.FileDescriptorSet{}

		var collectDeps func(fd *descriptorpb.FileDescriptorProto)
		collectDeps = func(fd *descriptorpb.FileDescriptorProto) {
			if fd == nil || seen[fd.GetName()] {
				return
			}
			seen[fd.GetName()] = true
			fdSet.File = append(fdSet.File, fd)
			for _, dep := range fd.Dependency {
				depFD, _ := refClient.FileByFilename(dep)
				if depFD != nil {
					collectDeps(depFD.AsFileDescriptorProto())
				}
			}
		}

		serviceMethods := svcDesc.GetMethods()
		for _, method := range serviceMethods {
			collectDeps(method.GetInputType().GetFile().AsFileDescriptorProto())
			collectDeps(method.GetOutputType().GetFile().AsFileDescriptorProto())
		}

		files, err := protodesc.NewFiles(fdSet)
		if err != nil {
			return fmt.Errorf("build file descriptor set: %w", err)
		}

		msgDescMap := buildMessageIndex(files)
		resolver := dynamicpb.NewTypes(files)
		for _, method := range serviceMethods {
			fullMethod := fmt.Sprintf("/%s/%s", serviceName, method.GetName())
			reqDesc := msgDescMap[method.GetInputType().GetFullyQualifiedName()]
			respDesc := msgDescMap[method.GetOutputType().GetFullyQualifiedName()]
			if reqDesc == nil || respDesc == nil {
				continue
			}

			pkg := svcDesc.GetFile().GetPackage()
			req, resp := reqDesc, respDesc
			methodsByName[fullMethod] = &MethodDescriptor{
				Contract:    fmt.Sprintf("%t/%t/%s", method.IsClientStreaming(), method.IsServerStreaming(), methodContract(req, resp)),
				FullMethod:  fullMethod,
				Package:     pkg,
				Service:     serviceName,
				Method:      method.GetName(),
				NewRequest:  func() proto.Message { return dynamicpb.NewMessage(req) },
				NewResponse: func() proto.Message { return dynamicpb.NewMessage(resp) },
				Resolver:    resolver,
			}
		}
	}

	return nil
}

func (p *ReflectionProxy) discoverAndRegister() error {
	if p == nil {
		return errors.New("reflection proxy is nil")
	}
	schema, err := discoverReflectionSchema(context.Background(), p.conn)
	if err != nil {
		return err
	}
	p.schema = schema
	return nil
}

func buildMessageIndex(files *protoregistry.Files) map[string]protoreflect.MessageDescriptor {
	idx := make(map[string]protoreflect.MessageDescriptor)
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		collectMessages(fd.Messages(), idx)
		return true
	})
	return idx
}

func collectMessages(messages protoreflect.MessageDescriptors, idx map[string]protoreflect.MessageDescriptor) {
	for i := 0; i < messages.Len(); i++ {
		msg := messages.Get(i)
		idx[string(msg.FullName())] = msg
		collectMessages(msg.Messages(), idx)
	}
}

// Invoke preserves the JSON-only API. Use InvokeWithHeaders to collect response metadata.
func (p *ReflectionProxy) Invoke(ctx context.Context, fullMethod string, jsonReq []byte) ([]byte, error) {
	body, _, err := p.InvokeWithHeaders(ctx, fullMethod, jsonReq)
	return body, err
}

// InvokeWithHeaders returns initial response metadata even when the RPC fails.
// Trailers are intentionally not bridged to HTTP response headers.
func (p *ReflectionProxy) InvokeWithHeaders(ctx context.Context, fullMethod string, jsonReq []byte) ([]byte, metadata.MD, error) {
	var cached *MethodDescriptor
	if p != nil && p.schema != nil {
		cached = p.schema.methods[fullMethod]
	}
	ok := cached != nil
	if !ok {
		if p != nil && p.app != nil && p.app.Debug {
			return nil, nil, fmt.Errorf("method not found: %s", fullMethod)
		}
		return nil, nil, core.ErrNotFound
	}

	desc := cached

	req := desc.NewRequest()
	if err := protoJSONUnmarshal(jsonReq, req, desc.Resolver); err != nil {
		core.Erro("[Gateway] gRPC request parse failed: grpc=%s json_bytes=%d err=%v", fullMethod, len(jsonReq), err)
		return nil, nil, status.Errorf(codes.InvalidArgument, "parse request failed: %v", err)
	}

	resp := desc.NewResponse()
	var headers metadata.MD
	if err := p.conn.Invoke(ctx, fullMethod, req, resp, grpc.Header(&headers)); err != nil {
		return nil, headers, err
	}

	body, err := protoJSONMarshal(resp, desc.Resolver)
	return body, headers, err
}

func protoJSONUnmarshal(data []byte, msg proto.Message, resolver interface {
	protoregistry.MessageTypeResolver
	protoregistry.ExtensionTypeResolver
}) error {
	data = normalizeJSONBytesFields(data, msg.ProtoReflect().Descriptor())
	data = promoteFlatMessageFields(data, msg.ProtoReflect().Descriptor())
	return (&protojson.UnmarshalOptions{
		DiscardUnknown: true,
		Resolver:       resolver,
	}).Unmarshal(data, msg)
}

func normalizeJSONBytesFields(data []byte, desc protoreflect.MessageDescriptor) []byte {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return data
	}

	changed := false
	fields := desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() != protoreflect.BytesKind || field.IsList() || field.IsMap() {
			continue
		}

		if key, raw, ok := rawFieldValue(values, field); ok && !rawJSONIsString(raw) {
			values[key] = rawJSONBytesValue(raw)
			changed = true
			continue
		}

		fieldName := string(field.Name())
		if !strings.HasSuffix(fieldName, "_json") {
			continue
		}
		alias := strings.TrimSuffix(fieldName, "_json")
		if _, raw, ok := rawFieldValue(values, field); ok && len(raw) > 0 {
			continue
		}
		raw, ok := values[alias]
		if !ok || len(raw) == 0 {
			continue
		}
		values[field.JSONName()] = rawJSONBytesValue(raw)
		delete(values, alias)
		changed = true
	}

	if !changed {
		return data
	}
	normalized, err := json.Marshal(values)
	if err != nil {
		return data
	}
	return normalized
}

func rawFieldValue(values map[string]json.RawMessage, field protoreflect.FieldDescriptor) (string, json.RawMessage, bool) {
	if value, ok := values[field.JSONName()]; ok {
		return field.JSONName(), value, true
	}
	name := string(field.Name())
	value, ok := values[name]
	return name, value, ok
}

func rawJSONIsString(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func rawJSONBytesValue(raw json.RawMessage) json.RawMessage {
	encoded, _ := json.Marshal(base64.StdEncoding.EncodeToString(raw))
	return encoded
}

func promoteFlatMessageFields(data []byte, desc protoreflect.MessageDescriptor) []byte {
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return data
	}

	changed := false
	fields := desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() != protoreflect.MessageKind || field.IsList() || field.IsMap() {
			continue
		}

		key, raw, ok := fieldValue(values, field)
		if !ok || raw == nil {
			continue
		}
		if _, alreadyNested := raw.(map[string]any); alreadyNested {
			continue
		}

		childFields := field.Message().Fields()
		anchor := childFields.ByJSONName(key)
		if anchor == nil {
			anchor = childFields.ByName(protoreflect.Name(key))
		}
		if anchor == nil || anchor.Kind() == protoreflect.MessageKind {
			continue
		}

		nested := map[string]any{anchor.JSONName(): raw}
		delete(values, key)
		for j := 0; j < childFields.Len(); j++ {
			child := childFields.Get(j)
			if child == anchor || topLevelField(fields, child.JSONName()) != nil {
				continue
			}
			if childKey, childValue, exists := fieldValue(values, child); exists {
				nested[child.JSONName()] = childValue
				delete(values, childKey)
			}
		}
		values[field.JSONName()] = nested
		changed = true
	}

	if !changed {
		return data
	}
	normalized, err := json.Marshal(values)
	if err != nil {
		return data
	}
	return normalized
}

func fieldValue(values map[string]any, field protoreflect.FieldDescriptor) (string, any, bool) {
	if value, ok := values[field.JSONName()]; ok {
		return field.JSONName(), value, true
	}
	name := string(field.Name())
	value, ok := values[name]
	return name, value, ok
}

func topLevelField(fields protoreflect.FieldDescriptors, name string) protoreflect.FieldDescriptor {
	if field := fields.ByJSONName(name); field != nil {
		return field
	}
	return fields.ByName(protoreflect.Name(name))
}

func protoJSONMarshal(msg proto.Message, resolver interface {
	protoregistry.MessageTypeResolver
	protoregistry.ExtensionTypeResolver
}) ([]byte, error) {
	return (&protojson.MarshalOptions{
		UseProtoNames: true,
		Resolver:      resolver,
	}).Marshal(msg)
}

// Methods 返回所有已注册的方法
func (p *ReflectionProxy) Methods() map[string]*MethodDescriptor {
	if p == nil || p.schema == nil {
		return map[string]*MethodDescriptor{}
	}
	result := make(map[string]*MethodDescriptor, len(p.schema.methods))
	for name, descriptor := range p.schema.methods {
		result[name] = descriptor
	}
	return result
}

func (p *ReflectionProxy) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		if p.closeFn != nil {
			p.closeErr = p.closeFn()
			return
		}
		if p.conn != nil {
			p.closeErr = p.conn.Close()
		}
	})
	return p.closeErr
}

// Fingerprint codecs by reachable types rather than the entire proto file, so
// adding an unrelated RPC does not make an unchanged method incompatible.
func methodContract(req, resp protoreflect.MessageDescriptor) string {
	types := map[string][]byte{}
	var collect func(protoreflect.MessageDescriptor)
	collect = func(md protoreflect.MessageDescriptor) {
		name := string(md.FullName())
		if _, seen := types[name]; seen {
			return
		}
		data, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(protodesc.ToDescriptorProto(md))
		types[name] = append([]byte(md.ParentFile().Syntax().String()), data...)
		for i := 0; i < md.Fields().Len(); i++ {
			field := md.Fields().Get(i)
			if field.Message() != nil {
				collect(field.Message())
			}
			if enum := field.Enum(); enum != nil {
				data, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(protodesc.ToEnumDescriptorProto(enum))
				types[string(enum.FullName())] = data
			}
		}
	}
	collect(req)
	collect(resp)
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	hash.Write([]byte(string(req.FullName()) + "\x00" + string(resp.FullName()) + "\x00"))
	for _, name := range names {
		hash.Write([]byte(name + "\x00"))
		hash.Write(types[name])
	}
	return hex.EncodeToString(hash.Sum(nil))
}
func reflectionFingerprint(methods map[string]*MethodDescriptor) string {
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		hash.Write([]byte(name + "\x00" + methods[name].Contract + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
