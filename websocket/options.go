package websocket

import (
	"net/http"
	"time"
)

// Options 按连接限制协议、来源、消息大小和网络等待；零值保留既有默认行为。
// ReadLimit 约束完整消息（包括分片）的累计字节数；超限不调用 OnMessage。
// RequireSubprotocol 要求客户端至少提供一个配置中的协议；配置为空时拒绝升级。
// ReadTimeout 是接收消息/心跳的空闲期限；每条消息开始及收到 pong 时刷新。
type Options struct {
	Subprotocols       []string
	RequireSubprotocol bool
	CheckOrigin        func(*http.Request) bool
	ReadLimit          int64
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	HandshakeTimeout   time.Duration
}

// WithOptions 在注册 Handler 前配置；协议切片复制，调用者后续修改不影响当前配置。
func (w *WS) WithOptions(options Options) *WS {
	options.Subprotocols = append([]string(nil), options.Subprotocols...)
	w.options = options
	return w
}
