// gateway/reflection_proxy.go
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
	"google.golang.org/protobuf/types/dynamicpb"
)

type ReflectionProxy struct {
	conn        *grpc.ClientConn
	methodCache sync.Map
	addr        string
	serviceName string
}

type MethodDescriptor struct {
	FullMethod  string
	NewRequest  func() proto.Message
	NewResponse func() proto.Message
}

func NewReflectionProxy(addr string) (*ReflectionProxy, error) {
	log.Printf("[ReflectionProxy] 连接服务: %s", addr)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("连接失败: %w", err)
	}

	p := &ReflectionProxy{
		conn: conn,
		addr: addr,
	}

	if err := p.discoverAndRegister(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发现服务失败: %w", err)
	}

	count := p.methodCount()
	log.Printf("[ReflectionProxy] ✅ 服务 %s 初始化完成，已注册 %d 个方法", addr, count)
	return p, nil
}

func (p *ReflectionProxy) discoverAndRegister() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 使用 grpcreflect 包（需要先安装）
	// go get github.com/jhump/protoreflect/grpcreflect
	stub := grpc_reflection_v1alpha.NewServerReflectionClient(p.conn)
	refClient := grpcreflect.NewClient(ctx, stub)

	// 获取所有服务
	services, err := refClient.ListServices()
	if err != nil {
		return fmt.Errorf("列出服务失败: %w", err)
	}

	log.Printf("[ReflectionProxy] 发现服务: %v", services)

	// 为每个服务加载方法
	for _, serviceName := range services {
		if strings.Contains(serviceName, "grpc.reflection") {
			continue
		}

		// 获取服务描述符
		svcDesc, err := refClient.ResolveService(serviceName)
		if err != nil {
			log.Printf("[ReflectionProxy] 解析服务 %s 失败: %v", serviceName, err)
			continue
		}

		// 注册所有方法
		for i := 0; i < svcDesc.GetMethodCount(); i++ {
			method := svcDesc.GetMethod(i)
			fullMethod := fmt.Sprintf("/%s/%s", serviceName, method.GetName())

			// 创建动态消息工厂
			reqDesc := method.GetInputType()
			respDesc := method.GetOutputType()

			desc := &MethodDescriptor{
				FullMethod: fullMethod,
				NewRequest: func() proto.Message {
					return dynamicpb.NewMessage(reqDesc)
				},
				NewResponse: func() proto.Message {
					return dynamicpb.NewMessage(respDesc)
				},
			}

			p.methodCache.Store(fullMethod, desc)
			log.Printf("[ReflectionProxy] 注册方法: %s", fullMethod)
		}
		p.serviceName = serviceName
	}

	return nil
}

func (p *ReflectionProxy) Invoke(ctx context.Context, fullMethod string, jsonReq []byte) ([]byte, error) {
	cached, ok := p.methodCache.Load(fullMethod)
	if !ok {
		return nil, fmt.Errorf("方法未注册: %s", fullMethod)
	}

	desc := cached.(*MethodDescriptor)

	req := desc.NewRequest()
	unmarshaler := protojson.UnmarshalOptions{
		DiscardUnknown: true,
	}
	if err := unmarshaler.Unmarshal(jsonReq, req); err != nil {
		return nil, fmt.Errorf("解析请求失败: %w", err)
	}

	resp := desc.NewResponse()
	if err := p.conn.Invoke(ctx, fullMethod, req, resp); err != nil {
		return nil, err
	}

	marshaler := protojson.MarshalOptions{
		UseProtoNames: true,
	}
	return marshaler.Marshal(resp)
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
