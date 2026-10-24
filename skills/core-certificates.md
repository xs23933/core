---
name: core-certificates
description: 动态精确域名的 ACME 证书申请、授权、HTTP-01、TLS 生命周期和续期
---

# 动态域名证书

自 Core v3.1.59 起可用。

租户自定义域名、动态 HTTPS、ACME 签发和续期使用 `github.com/xs23933/core/v3/certificate`。不得沿用旧的静态 `acme.domains` 或 Cloudflare `certmagic` 配置实现租户签发。

```go
manager, err := certificate.New(certificate.Config{
    StorageDir: "/var/lib/app/certificates",
    Email: "ops@example.com",
    Authorize: func(ctx context.Context, hostname string) error {
        // 必须查询业务权威的已验证、启用且未撤销的精确域名绑定。
        return domains.Authorize(ctx, hostname)
    },
})
if err != nil { return err }
if err = app.UseCertificateManager(manager, ":80"); err != nil {
    if closeErr := manager.Close(); closeErr != nil { return closeErr }
    return err
}
// app 的 listen 配置指定 HTTPS 地址，例如 :443。
// 同步业务已验证域名清单后，后台触发申请；Ensure 不等待 CA。
if err = manager.Ensure(ctx, "pay.customer.com"); err != nil { return err }
return app.Run()
```

`Authorize` 必须显式提供，失败或未知一律拒绝。管理器在 Ensure、实际申请/续期前、申请结束、TLS 握手和 HTTP-01 请求检查授权。授权回调应快速返回并遵守 context；多实例必须读取当前权威事实，不能仅依赖过期启动清单。业务绑定 generation 和租户归属由业务方管理。

`Remove(ctx, hostname)` 取消当前操作、拒绝新握手和后续续期，不删除专用存储或向 CA 撤销证书。已建立连接由业务权限层处理；TLS session tickets 禁用以阻止授权撤销后的会话恢复。重新绑定须重新授权并 Ensure；前一代异步结果不会恢复已移除条目。

`Status(ctx, hostname)` 返回 Hostname、State、ErrorCode、NotBefore、NotAfter 和 UpdatedAt。State 为 pending、issuing、ready、renewing、failed、expired、removed；未知域名返回 ErrUnknown，关闭后返回 ErrClosed。失败码只含 issuance_failed、renewal_failed、unauthorized，不暴露 CA 错误、账号信息或密钥。续期失败仍可服务旧的有效证书；过期或尚未生效证书不能用于握手。重复 Ensure 不重置退避截止时间。

实际引擎使用专用 CertMagic 配置、HTTP-01 和精确单 SAN hostname，不启用按需签发、不转换泛域名、不修改 CertMagic 包级默认值。证书不注册到 CertMagic 自动续期缓存，管理器每分钟检查，并在最后三分之一有效期内续期；CheckInterval 与 OperationTimeout 可配置。重启时业务方重新同步清单并 Ensure，从持久存储恢复证书，清单不由 Core 保存。

默认生产 CA；测试/预发布通过 Config.CA 指定 staging 或本地 ACME directory。测试使用注入 Engine 或本地证书 fixture，不访问公网 CA。Engine 合同为 Load、Obtain、HTTPHandler 和 Close；Obtain 必须遵守取消并返回解析过 Leaf 的精确 hostname 证书。

StorageDir 必须是持久化的专用目录；默认创建权限 0700，证书和私钥交给 CertMagic FileStorage。多实例可通过 Config.Storage 注入共享 CertMagic Storage，必须实现可靠的分布式锁和原子写入；每个接收挑战的实例都必须同步域名并运行同一授权策略。不得把私钥存入普通业务数据库、etcd、Outbox、NSQ 或日志。

UseCertificateManager 必须在 Run/Serve 前调用，与已有 TLSConfig、acme 或 certmagic 冲突时返回错误。成功后 Core 托管 manager.Close 和可选 HTTP listener；失败时调用方负责关闭 manager。HTTPS 仍由原有 listener 提供，不另启 HTTPS 或 HTTP/3 listener。HTTP 地址必须由公网 80 转发可达；HTTP handler 只处理挑战，其它路径返回 404，不提供无条件重定向。

如果 HTTP-01 由其它进程/入口转发，可用空 httpAddress，另将 manager.HTTPHandler(next) 接到实际接收挑战的入口。内置引擎始终预占独立的 127.0.0.1 临时端口，并显式将 CertMagic HTTP-01 solver 指向该已监听端口，避免 solver 自行绑定公网 80。内部 listener 默认拒绝请求，管理器接入后也必须经过相同的域名成员、Remove/关闭状态和实时 Authorize 检查；公开挑战入口使用 :80、其它端口或外部转发时均执行相同约束。引擎关闭时释放内部 listener。多实例共享存储只解决证书与挑战数据协调，仍须部署正确的公网挑战路由。

## 既有静态 CertMagic 的维护

修复旧版 `certmagic` / on-demand 时，独立 Cache 的 `GetConfigForCert` 必须返回用于首次管理和 TLS 握手的同一个 Config，保留 Storage、Issuers、DNS solver 和 subject transformer；不得用共享 `NewDefault()` 缓存配合局部覆盖。失败及关闭路径停止缓存。回归应实际调用 `Cache.RenewManagedCertificates`，确认从实例目录重载新证书，避免仅测试同步续期。外部 Caddy/Nginx 手动加载 PEM 文件时，需要部署层另行重载。
