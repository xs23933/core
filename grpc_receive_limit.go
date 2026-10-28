package core

const defaultGRPCMaxRecvMessageBytes = 4 << 20
const maximumGRPCMaxRecvMessageBytes = 64 << 20

// GRPCMaxRecvMessageBytes 返回通用 gRPC 接收上限，单位为字节。
// 未配置、非正值或超过64MiB均安全回落4MiB；不会把零解释为无限制。
func (app *Core) GRPCMaxRecvMessageBytes() int {
	if app == nil || app.Conf == nil {
		return defaultGRPCMaxRecvMessageBytes
	}
	limit := app.Conf.GetInt64("grpc.max_recv_message_bytes", defaultGRPCMaxRecvMessageBytes)
	if limit <= 0 || limit > maximumGRPCMaxRecvMessageBytes {
		return defaultGRPCMaxRecvMessageBytes
	}
	return int(limit)
}
