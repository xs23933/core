package etcd

import (
	"fmt"
	"strings"
	"time"
)

type Options struct {
	// Namespace 隔离共享 etcd 中不同项目的数据；为空时保持旧 key 布局
	Namespace string

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

// NamespacePrefix 将逻辑前缀放入规范化后的项目命名空间。
func NamespacePrefix(namespace, logicalPrefix string) string {
	segments := strings.FieldsFunc(strings.TrimSpace(namespace), func(r rune) bool {
		return r == '/'
	})
	logical := strings.Trim(strings.TrimSpace(logicalPrefix), "/")
	parts := make([]string, 0, len(segments)+1)
	parts = append(parts, segments...)
	if logical != "" {
		parts = append(parts, logical)
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/") + "/"
}

// ServiceRoot 返回当前命名空间下的服务根前缀。
func (o *Options) ServiceRoot() string {
	if o == nil {
		return NamespacePrefix("", "services")
	}
	return NamespacePrefix(o.Namespace, "services")
}

// ConfigPrefix 返回当前命名空间下的配置根前缀。
func (o *Options) ConfigPrefix() string {
	if o == nil {
		return NamespacePrefix("", "config")
	}
	return NamespacePrefix(o.Namespace, "config")
}

// 生成 etcd key
func (o *Options) ServiceKey() string {
	if o.ServiceID != "" {
		return fmt.Sprintf("%s%s/%s", o.ServiceRoot(), o.ServiceName, o.ServiceID)
	}
	return fmt.Sprintf("%s%s/%s", o.ServiceRoot(), o.ServiceName, o.ServiceAddr)
}

// 服务前缀（用于发现）
func (o *Options) ServicePrefix() string {
	return fmt.Sprintf("%s%s/", o.ServiceRoot(), o.ServiceName)
}
