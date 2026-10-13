# 托管工作负载身份

Core 可在 Gateway 进程启动单活私有 CA 签发器；全部后端自动申请、保存和续期本进程证书，普通 gRPC、专用 listener 和 Gateway reflection 共用身份。此能力为显式启用；未启用的应用保持原有 transport API。

## 启动合同

在任何 `RegisterGRPCService`、`GetGRPCServer`、`GrpcClient` 或 `GrpcClientAt` 之前调用：

```go
if err := app.EnableWorkloadIdentity(); err != nil {
    return err
}
// 业务拦截器照常配置；托管模式不传 TransportCredentials。
if err := app.ConfigureGRPCServer(core.GRPCServerConfig{
    UnaryInterceptors: []grpc.UnaryServerInterceptor{authorize},
}); err != nil {
    return err
}
```

身份准备失败不能回退明文。未初始化时直接取得 gRPC server 也只能拒绝握手。准备完成后照常注册业务服务、`EnableEtcdRegistry(nil)`、`Run`。HTTP 和 gRPC 使用独立 listener，托管模式不支持 HTTP/gRPC 共端口。普通客户端仍使用 `app.GrpcClient("ledger")` 或 `app.GrpcClientAt("ledger", address)`，无需逐目标 transport 配置；启用托管模式后显式 transport/旧 DialOption 与托管配置冲突并返回错误。

## 配置

```yaml
workload:
  enabled: true
  trust_domain: example.internal
  # 默认使用 etcd.service_name；没有注册的进程可显式设置。
  service_name: gateway
  issuer_url: https://gateway-internal:9444
  trust_bundle_file: /run/core-trust/roots.pem
  bootstrap_token_file: /run/core-secrets/gateway.token
  state_dir: /var/lib/core/identity
  # 默认在有效期约一半时续期，并加入提前抖动。
  retry_interval: 1m

# 仅签发器进程配置以下段；由 Go 首次创建 CA，无需 OpenSSL。
workload_issuer:
  enabled: true
  listen: :9444
  advertise_url: https://gateway-internal:9444
  state_dir: /var/lib/core/issuer
  policy_file: /run/core-secrets/enrollment-policy.json
  # 独立于签发状态卷保存，防止丢卷后误建新根。
  pin_file: /var/lib/core-pin/root.pin
  leaf_ttl: 168h
```

Gateway 自身使用本地签发，不向自己的 HTTP 入口申请；其 `trust_bundle_file` 指向签发器初始化导出的 `<state_dir>/root.pem`。后端的公共根证书由可信部署渠道分发，不使用未认证下载或 etcd 中的任意值建立首次信任。

授权文件格式为 `{"services":{"ledger":{"token_sha256":"<令牌SHA256十六进制摘要>"}}}`。每服务使用独立高熵令牌（至少 32 字节），令牌文件权限 0600，状态目录 0700。服务 URI 为 `spiffe://<trust_domain>/<service_name>`，证书唯一 URI SAN 且含 ServerAuth/ClientAuth。域与服务名严格校验；具体 RPC 授权仍由业务拦截器执行。同 CA 不代表允许调用全部服务方法。

签发器 HTTPS `/enroll` 接受专属 Bearer 引导认证；`/renew` 要求已验证客户端证书且只能续签自身身份。JSON 请求字段为 `service`、`csr_pem`、`request_id`，响应为 `certificate_pem`、`ca_pem`。私钥在后端生成，CSR 不得要求自选 SAN/扩展。后端仅信任预置根，响应 `ca_pem` 不改变信任。

## 保存、故障和轮换

- 首次启动以独占锁生成根、中间 CA 和入口证书，保存 `issuer.json`、`root-key.pem`、公共 `root.pem` 及独立指纹。后续复用已有根；损坏、部分丢失、根指纹不符或已有 pin 但状态丢失都拒绝自动重建。初始化多文件提交被中断且已写 pin 时也需受控恢复，不静默换根。
- 后端使用单个原子 `identity.json` 同时保存私钥、证书和待申请幂等事实，避免证书/私钥分开覆盖。进程独占状态目录；每个副本使用自己的卷和密钥。重启加载有效证书，连接及 RPC 不申请新证书。
- 签发先持久化幂等及审计事实，再返回成功；同键同 CSR 返回原证书，同键跨身份或不同 CSR 拒绝。到期的幂等结果在校验确为可信自身证书后重新申请新键，不无限重试旧结果。
- 续期采用提前抖动、单任务和有界指数退避。签发器离线不影响已有有效身份直接通信；独立到期监视不会被网络申请阻塞。Core 到期撤注册并在五秒内关闭业务 transport，不让旧连接无限延续身份。
- 默认 gRPC server 连接最大年龄一小时、排空宽限五分钟；到期关停期限独立生效。专用 listener 调用 `WorkloadServerCredentials()` / `WorkloadClientCredentials(target)`，其 owner 仍须设置有界连接寿命并接入 shutdown。
- 签发入口 HTTPS 证书由本地 CA 自动更新，不经网络自调用。根私钥可转离线保管，日常签发使用中间 CA；可使用 `app.RotateWorkloadIntermediate()` 受控更新中间 CA（需要本地根私钥），新旧叶子继续由同一根验证；根私钥离线时明确拒绝，需离线签发并受控安装。根 CA 更新不自动执行。
- 根信任包仅启动时读取；根更新先部署新旧根重叠包并滚动重启，更新签发材料和叶子，等待旧证书及连接退出，再移除旧根并滚动重启。禁止宣称文件替换即热更新。撤销引导令牌阻止后续申请/续期，不会立即吊销已有证书；紧急撤权使用业务授权或实例隔离。
- 签发器第一版单活；多个 Gateway 数据面可以共用一个签发入口，不能让多个签发器并发写本地状态。备份必须保护私钥并同时保存签发状态和独立 pin。

## etcd TLS 独立配置

业务 mTLS 不自动授予 etcd 权限。Registry、Discovery 和 Gateway 的自建 etcd client 统一读取：

```yaml
etcd:
  tls:
    enabled: true
    ca_file: /run/etcd-trust/ca.pem
    cert_file: /run/etcd-secrets/client.pem
    key_file: /run/etcd-secrets/client-key.pem
    server_name: etcd.internal
```

CA 必需，客户端证书/私钥必须成对；仅服务端 TLS 可省略客户端证书。启用 TLS 使用 TLS 1.3，配置错误直接失败。etcd 的用户映射和 RBAC 由部署管理，不把 `service_name` 当成授权身份。

`app.WorkloadIdentityStats()` 提供申请尝试/成功/失败、到期时间和剩余寿命；`app.WorkloadIssuerStats()` 提供签发、幂等命中和拒绝计数。它们是无秘密的进程内快照，监控 owner 自行接入，不新增外部业务接口。
