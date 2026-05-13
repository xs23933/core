package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jhump/protoreflect/grpcreflect"
	"github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

type ReflectionProxy struct {
	conn        *grpc.ClientConn
	methodCache sync.Map
	addr        string
	app         *core.Core
}

type MethodDescriptor struct {
	FullMethod  string
	Package     string // proto package, e.g. "v1.auth"
	Service     string // service name, e.g. "UserService"
	Method      string // method name, e.g. "PostLogin"
	NewRequest  func() proto.Message
	NewResponse func() proto.Message
}

func NewReflectionProxy(app *core.Core, addr string) (*ReflectionProxy, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create client conn failed: %w", err)
	}

	p := &ReflectionProxy{conn: conn, addr: addr, app: app}

	if err := p.discoverAndRegister(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("discover and register failed: %w", err)
	}
	return p, nil
}

func (p *ReflectionProxy) discoverAndRegister() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stub := grpc_reflection_v1alpha.NewServerReflectionClient(p.conn)
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
			core.Erro("[ReflectionProxy] resolve service %s failed: %v", serviceName, err)
			continue
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
			// 递归收集所有依赖
			for _, dep := range fd.Dependency {
				depFD, _ := refClient.FileByFilename(dep)
				if depFD != nil {
					collectDeps(depFD.AsFileDescriptorProto())
				}
			}
		}

		methods := svcDesc.GetMethods()
		for _, method := range methods {
			collectDeps(method.GetInputType().GetFile().AsFileDescriptorProto())
			collectDeps(method.GetOutputType().GetFile().AsFileDescriptorProto())
		}

		// 一次构建 FileDescriptor
		files, err := protodesc.NewFiles(fdSet)
		if err != nil {
			core.Erro("[ReflectionProxy] build file descriptor set failed: %v", err)
			continue
		}

		// 预构建消息描述符查找表
		msgDescMap := buildMessageIndex(files)

		// 注册所有方法
		for _, method := range methods {
			fullMethod := fmt.Sprintf("/%s/%s", serviceName, method.GetName())

			reqDesc := msgDescMap[method.GetInputType().GetFullyQualifiedName()]
			respDesc := msgDescMap[method.GetOutputType().GetFullyQualifiedName()]

			if reqDesc == nil || respDesc == nil {
				continue
			}

			// 提取 proto package
			pkg := svcDesc.GetFile().GetPackage()

			req, resp := reqDesc, respDesc // capture for closure
			p.methodCache.Store(fullMethod, &MethodDescriptor{
				FullMethod:  fullMethod,
				Package:     pkg,
				Service:     serviceName,
				Method:      method.GetName(),
				NewRequest:  func() proto.Message { return dynamicpb.NewMessage(req) },
				NewResponse: func() proto.Message { return dynamicpb.NewMessage(resp) },
			})
		}
	}

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

func (p *ReflectionProxy) Invoke(ctx context.Context, fullMethod string, jsonReq []byte) ([]byte, error) {
	cached, ok := p.methodCache.Load(fullMethod)
	if !ok {
		if p.app.Debug {
			return nil, fmt.Errorf("method not found: %s", fullMethod)
		}
		return nil, core.ErrNotFound
	}

	desc := cached.(*MethodDescriptor)

	req := desc.NewRequest()
	if err := (&protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(jsonReq, req); err != nil {
		return nil, fmt.Errorf("parse request failed: %w", err)
	}

	resp := desc.NewResponse()
	if err := p.conn.Invoke(ctx, fullMethod, req, resp); err != nil {
		return nil, err
	}

	return (&protojson.MarshalOptions{UseProtoNames: true}).Marshal(resp)
}

// Methods 返回所有已注册的方法
func (p *ReflectionProxy) Methods() map[string]*MethodDescriptor {
	result := make(map[string]*MethodDescriptor, p.methodCount())
	p.methodCache.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(*MethodDescriptor)
		return true
	})
	return result
}

func (p *ReflectionProxy) methodCount() int {
	count := 0
	p.methodCache.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

func (p *ReflectionProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}
