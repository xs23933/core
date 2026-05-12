package etcd

import (
	"fmt"
	"time"
)

type Options struct {
	// etcd 客户端配置
	Endpoints   []string
	Username    string
	Password    string
	DialTimeout time.Duration

	// 服务注册配置
	ServiceName string
	ServiceAddr string
	ServiceID   string
	TTL         int64 // 租约时间（秒）
	Version     string

	// 元数据
	Metadata map[string]string
}

func DefaultOptions() *Options {
	return &Options{}
}

// 生成 etcd key
func (o *Options) ServiceKey() string {
	if o.ServiceID != "" {
		return fmt.Sprintf("/services/%s/%s", o.ServiceName, o.ServiceID)
	}
	return fmt.Sprintf("/services/%s/%s", o.ServiceName, o.ServiceAddr)
}

// 服务前缀（用于发现）
func (o *Options) ServicePrefix() string {
	return fmt.Sprintf("/services/%s/", o.ServiceName)
}
