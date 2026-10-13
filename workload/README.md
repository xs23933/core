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

通过 `core.LoadConfigFile` 加载 YAML 或加密 `.dat` 时，`workload.state_dir`、`trust_bundle_file`、`bootstrap_token_file` 以及 `workload_issuer.state_dir`、`policy_file`、`pin_file` 的非空相对路径，在环境变量展开后按该配置文件所在目录解析。`state_dir: ./workload` 因而始终位于配置旁，不随启动 cwd 或 `-f`/`--config` 的绝对、相对写法变化。绝对路径保持原值；直接以程序构造 `Options` 或调用 `workload.Open` 时，仍由调用方确定路径基准。其它业务配置路径不受此规则影响。

```yaml
workload:
  enabled: true
  trust_domain: example.internal
  # 默认使用 etcd.service_name；没有注册的进程可显式设置。
  service_name: ledger
  issuer_url: https://gateway-internal:9444
  # 根证书 DER 的 SHA-256，必须通过可信部署预置。
  root_sha256: "<64位十六进制指纹>"
  bootstrap_token_file: /run/core-secrets/ledger.token
  state_dir: ./workload
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

跨服务器后端预置 `workload.root_sha256`、签发地址和专属引导凭据，不需要复制根文件或共享 Gateway 的磁盘。根缓存默认 `<workload.state_dir>/root.pem`；如指定 `trust_bundle_file`，该路径仅属于本机缓存。已有缓存每次启动仍核验指纹，损坏或不符时失败，不从网络静默覆盖。

首次缺缓存时，Core 从同一签发入口的 HTTPS `GET /root.pem` 获取公共根证书。引导下载不发送 token、Cookie 或客户端证书，拒绝重定向、HTTP、环境代理、超限内容和无效根。先解析唯一自签名 CA，按证书 DER 计算 SHA-256 与可信配置比较，再使用该根完整验证本次 HTTPS 服务端链、DNS/IP SAN、用途及有效期，原子保存后才按正常 HTTPS 执行 enroll。下载阶段暂缓默认 TLS 验证仅限此公共根请求，不能复用于带凭据的申请/续期。

Gateway 自身使用本地签发，不向自己的 HTTP 入口申请；其 `workload.service_name` 为 `gateway`、`trust_bundle_file` 指向本机签发器导出的 `<workload_issuer.state_dir>/root.pem`，不需要通过尚未启动的签发入口下载自己的根。上面的 `workload_issuer` 段仅在签发进程启用，后端不配置它。无指纹的显式本地 `trust_bundle_file` 模式保留给已有 Core 调用方，WPay 后端统一使用指纹引导。

授权文件格式为 `{"services":{"ledger":{"token_sha256":"<令牌SHA256十六进制摘要>"}}}`。每服务使用独立高熵令牌（至少 32 字节），令牌文件权限 0600，状态目录 0700。服务 URI 为 `spiffe://<trust_domain>/<service_name>`，证书唯一 URI SAN 且含 ServerAuth/ClientAuth。域与服务名严格校验；具体 RPC 授权仍由业务拦截器执行。同 CA 不代表允许调用全部服务方法。

签发器内部 HTTPS `GET/HEAD /root.pem` 公开返回根证书，不暴露私钥或策略，不发布为普通业务 Gateway 路由。`/enroll` 接受专属 Bearer 引导认证；`/renew` 要求已验证客户端证书且只能续签自身身份。JSON 请求字段为 `service`、`csr_pem`、`request_id`，响应为 `certificate_pem`、`ca_pem`。私钥在后端生成，CSR 不得要求自选 SAN/扩展。后端仅信任预置指纹确认的根或显式可信本地根，响应 `ca_pem` 不改变信任。

## 保存、故障和轮换

- 首次启动以独占锁生成根、中间 CA 和入口证书，保存 `issuer.json`、`root-key.pem`、公共 `root.pem` 及独立指纹。后续复用已有根；损坏、部分丢失、根指纹不符或已有 pin 但状态丢失都拒绝自动重建。初始化多文件提交被中断且已写 pin 时也需受控恢复，不静默换根。
- 后端使用单个原子 `identity.json` 同时保存私钥、证书和待申请幂等事实，避免证书/私钥分开覆盖。进程独占状态目录；每个副本使用自己的卷和密钥。重启加载有效证书，连接及 RPC 不申请新证书。
- 签发先持久化幂等及审计事实，再返回成功；同键同 CSR 返回原证书，同键跨身份或不同 CSR 拒绝。到期的幂等结果在校验确为可信自身证书后重新申请新键，不无限重试旧结果。
- 续期采用提前抖动、单任务和有界指数退避。签发器离线不影响已有有效身份直接通信；独立到期监视不会被网络申请阻塞。Core 到期撤注册并在五秒内关闭业务 transport，不让旧连接无限延续身份。
- 默认 gRPC server 连接最大年龄一小时、排空宽限五分钟；到期关停期限独立生效。专用 listener 调用 `WorkloadServerCredentials()` / `WorkloadClientCredentials(target)`，其 owner 仍须设置有界连接寿命并接入 shutdown。
- 签发入口 HTTPS 证书由本地 CA 自动更新，不经网络自调用。根私钥可转离线保管，日常签发使用中间 CA；可使用 `app.RotateWorkloadIntermediate()` 受控更新中间 CA（需要本地根私钥），新旧叶子继续由同一根验证；根私钥离线时明确拒绝，需离线签发并受控安装。根 CA 更新不自动执行。
- 根信任仅启动准备时加载，不在线接受新的网络根或指纹。指纹模式需要显式部署新指纹及对应缓存/身份恢复方案后滚动重启，禁止自动覆盖不匹配的旧缓存；显式本地信任包模式可用新旧根重叠包完成受控轮换。禁止宣称文件替换即热更新。撤销引导令牌阻止后续申请/续期，不会立即吊销已有证书；紧急撤权使用业务授权或实例隔离。
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

## 指纹的部署来源

根指纹为根证书 DER（x509.Certificate.Raw）的 SHA-256，64 位十六进制；不是 PEM 文件字节的 hash，PEM 换行变化不改变证书指纹。首次 Gateway 初始化写入的 `workload_issuer.pin_file` 已保存同一指纹，可通过可信 SSH、部署配置或 secret/config 管理渠道预置到后端。指纹是公开信任锚，不从 `/root.pem` 或其它未认证网络响应自动获取其期望值。

后端跨机配置必须把默认回环 `issuer_url` 改为其可访问的内部 HTTPS DNS/IP，且该名称存在于 Gateway 签发入口证书 SAN。此能力不自动改变网络监听、DNS、防火墙、etcd 注册地址或 RPC 权限；部署按各自网络事实配置。有效根缓存与身份证书允许签发器离线重启；缓存缺失而下载失败时明确失败，不回退明文或跳过校验。

## HTTP-only 服务

工作负载身份同时支持只调用后端的 HTTP-only 服务。先启用 `EnableWorkloadIdentity`，随后 `EnableEtcdRegistry` 和 HTTP 路由发布即可，无需配置无用途的 `grpc.addr`。Registry 不创建 gRPC server；它仅为已通过 `RegisterGRPCService` 或 `GetGRPCServer` 创建的 server 开启 reflection。实例 `metadata.grpc_enabled=false` 使 Gateway 和 gRPC resolver 跳过入站 gRPC，但不影响 HTTP 代理或本进程出站 mTLS。该字段表示能力，不替代业务认证。真正提供 gRPC 的应用仍必须独立 mTLS 端口，共用 HTTP 端口会拒绝启动。
