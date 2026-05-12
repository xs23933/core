package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jhump/protoreflect/grpcreflect"
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
}

type MethodDescriptor struct {
	FullMethod  string
	NewRequest  func() proto.Message
	NewResponse func() proto.Message
}

func NewReflectionProxy(addr string) (*ReflectionProxy, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("连接失败: %w", err)
	}

	p := &ReflectionProxy{conn: conn, addr: addr}

	if err := p.discoverAndRegister(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发现服务失败: %w", err)
	}

	log.Printf("[ReflectionProxy] 服务 %s 初始化完成，已注册 %d 个方法", addr, p.methodCount())
	return p, nil
}

func (p *ReflectionProxy) discoverAndRegister() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stub := grpc_reflection_v1alpha.NewServerReflectionClient(p.conn)
	refClient := grpcreflect.NewClient(ctx, stub)
	defer refClient.Reset()

	services, err := refClient.ListServices()
	if err != nil {
		return fmt.Errorf("列出服务失败: %w", err)
	}

	// 按服务维度构建 FileDescriptorSet，避免重复创建
	for _, serviceName := range services {
		if strings.Contains(serviceName, "grpc.reflection") {
			continue
		}

		svcDesc, err := refClient.ResolveService(serviceName)
		if err != nil {
			log.Printf("[ReflectionProxy] 解析服务 %s 失败: %v", serviceName, err)
			continue
		}

		// 收集该服务所有方法涉及的 proto 文件（去重）
		seen := make(map[string]bool)
		fdSet := &descriptorpb.FileDescriptorSet{}

		methods := svcDesc.GetMethods()
		for _, method := range methods {
			for _, fd := range []*descriptorpb.FileDescriptorProto{
				method.GetInputType().GetFile().AsFileDescriptorProto(),
				method.GetOutputType().GetFile().AsFileDescriptorProto(),
			} {
				if fd != nil && !seen[fd.GetName()] {
					seen[fd.GetName()] = true
					fdSet.File = append(fdSet.File, fd)
				}
			}
		}

		// 一次构建 FileDescriptor
		files, err := protodesc.NewFiles(fdSet)
		if err != nil {
			log.Printf("[ReflectionProxy] 创建文件描述符失败: %v", err)
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
				log.Printf("[ReflectionProxy] 找不到消息类型: %s/%s",
					method.GetInputType().GetFullyQualifiedName(),
					method.GetOutputType().GetFullyQualifiedName())
				continue
			}

			req, resp := reqDesc, respDesc // capture for closure
			p.methodCache.Store(fullMethod, &MethodDescriptor{
				FullMethod: fullMethod,
				NewRequest:  func() proto.Message { return dynamicpb.NewMessage(req) },
				NewResponse: func() proto.Message { return dynamicpb.NewMessage(resp) },
			})
		}
	}

	return nil
}

// buildMessageIndex 构建 fullName -> MessageDescriptor 的索引，避免遍历查找
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
		return nil, fmt.Errorf("方法未注册: %s", fullMethod)
	}

	desc := cached.(*MethodDescriptor)

	req := desc.NewRequest()
	if err := (&protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(jsonReq, req); err != nil {
		return nil, fmt.Errorf("解析请求失败: %w", err)
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
