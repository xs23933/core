/*
*

e.g:

package main

import (

	"context"
	"fmt"
	"log"
	"os"
	"pkg/proto/auth/pb"
	"time"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

)

	func main() {
		fmt.Println("Starting client...")

		app := core.New(core.Options{
			"etcd": core.Options{
				"endpoints":   []string{"192.168.31.5:2379"},
				"dialTimeout": 5 * time.Second,
			},
		})

		conn, err := app.GrpcClient("auth-service", grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatal(err)
		}
		defer conn.Close()

		client := pb.NewUserServiceClient(conn)

		fmt.Println("Calling Login...")
		resp, err := client.Login(context.Background(), &pb.LoginRequest{
			Account:   "admin",
			Password:  "password",
			LoginType: pb.LoginType_LOGIN_TYPE_PASSWORD,
		})
		if err != nil {
			core.Erro("Login failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Login response: %v", resp)
	}
*/
package core

import (
	"errors"
	"sync"
	"time"

	"github.com/xs23933/core/v3/etcd"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var resolverInitOnce sync.Once

/*
*
etcd gRPC Client

----

e.g:

package main

import (

	"context"
	"fmt"
	"log"
	"os"
	"pkg/proto/auth/pb"
	"time"

	"github.com/xs23933/core/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

)

	func main() {
		fmt.Println("Starting client...")

		app := core.New(core.Options{
			"etcd": core.Options{
				"endpoints":   []string{"192.168.31.5:2379"},
				"dialTimeout": 5 * time.Second,
			},
		})

		conn, err := app.GrpcClient("auth-service", grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatal(err)
		}
		defer conn.Close()

		client := pb.NewUserServiceClient(conn)

		fmt.Println("Calling Login...")
		resp, err := client.Login(context.Background(), &pb.LoginRequest{
			Account:   "admin",
			Password:  "password",
			LoginType: pb.LoginType_LOGIN_TYPE_PASSWORD,
		})
		if err != nil {
			core.Erro("Login failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Login response: %v", resp)
	}
*/
func (app *Core) GrpcClient(serviceName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	if app.EtcdDiscovery == nil {
		if err := app.enableEtcdDiscoveryFromConf(); err != nil {
			return nil, err
		}
	}

	// init etcd resolver once
	resolverInitOnce.Do(func() {
		etcd.InitEtcdResolver(app.EtcdDiscovery)
	})

	// 添加默认的 insecure 凭证（如果没有设置的话）
	hasTransportCreds := false
	for _, opt := range opts {
		// 检查是否已经设置了传输凭证
		if opt != nil {
			// 简单检查：通常 grpc.WithTransportCredentials 会设置
			hasTransportCreds = true
		}
	}

	if !hasTransportCreds {
		// 添加 insecure 凭证用于开发/测试环境
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := etcd.Dial(serviceName, opts...)
	return conn, err
}

// enableEtcdDiscoveryFromConf 从配置自动启用
func (app *Core) enableEtcdDiscoveryFromConf() error {
	etcdConf := app.Conf.GetMap("etcd")
	if etcdConf == nil {
		return errors.New("etcd not configured")
	}

	opts := &etcd.Options{
		Endpoints:   etcdConf.GetStrings("endpoints", []string{"127.0.0.1:2379"}),
		DialTimeout: time.Duration(etcdConf.GetInt64("dialTimeout", 5)) * time.Second,
	}

	return app.EnableEtcdDiscovery(opts)
}
