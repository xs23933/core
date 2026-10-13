package fetch

import "time"

// Timeout 设置每次 HTTP 请求的总超时，包含连接、重定向和响应读取。
// 非正值沿用 New 的 30 秒默认；更短的父 context deadline 优先。
// 复制客户端配置后原子替换，不修改已开始请求使用的客户端。
func (f *Fetch) Timeout(timeout time.Duration) *Fetch {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	client := *f.httpClient()
	client.Timeout = timeout
	f.client.Store(&client)
	return f
}
