package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"golang.org/x/net/context"
)

/*
*
# 设置 CertMagic 使用 Cloudflare DNS-01 挑战

第一步：登录并进入创建页面

打开 [Cloudflare 控制台]（https：//dash.cloudflare.com/）并登录你的账户。

点击页面右上角的个人头像。

在下拉菜单中选择 “我的个人资料” （My Profile）。

在左侧导航栏中，点击 “API 令牌” （API Tokens） 标签页[citation：1][citation：8]。

点击 “创建令牌” （Create Token） 按钮。

第二步：选择模板
你会看到两种方式：

选择模板：为了简化操作，找到 “编辑区域 DNS” （Edit zone DNS） 模板，点击它右侧的 “使用模板” （Use template）。

注意：如果你需要完全精准的权限控制，也可以选择底部的“创建自定义令牌”（Create Custom Token），但使用模板后手动修改权限会更快捷。

跳过模板：如果你点了“创建自定义令牌”，请继续阅读下文配置权限。

第三步：配置令牌权限与资源（关键步骤）
你需要确保生成的令牌具备修改 DNS 记录的权限，因为 CertMagic 需要通过添加 TXT 记录来验证域名所有权。

令牌名称：给令牌起一个名字，例如 CertMagic-DNS-Challenge。

权限 （Permissions）：

根据官方文档，为了允许 CertMagic 自动添加和删除验证记录，你需要 DNS 写入 权限[citation：1][citation：5]。

第一项：

资源类型：选择 区域 （Zone）

权限：选择 DNS

访问级别：选择 编辑 （Edit）

（可选但推荐） 为了安全，你也可以只给 区域 （Zone） > 区域 （Zone） > 读取 （Read） 的权限，这通常不影响 DNS 验证。

区域资源 （Zone Resources）：

选择 “包含” （Include）。

在资源类型下拉框中，选择 “特定区域” （Specific zone）。

在下方选择框里，选中你具体要操作的域名（例如 dev.example.com）。

*如果你希望在同一个 Cloudflare 账号下的多个不同域名上使用这个令牌，可以改为选择“包含”（Include） -> “账户中的所有区域”（All zones from an account）[citation：5]。*

客户端 IP 地址过滤 （Client IP Address Filtering）：

这是一个安全选项。如果你知道你的服务器拥有固定的公网 IP，建议在这里填入你的 IP，限制只有该 IP 发出的请求才有效。如果不确定，可以留空[citation：1][citation：2]。

第四步：生成并保存令牌
确认设置无误后，点击 “继续至摘要” （Continue to summary）。

再次核对权限摘要，点击 “创建令牌” （Create Token）。

⚠️ 极其重要：页面会立即生成一个以 cfat_ 或 cfut_ 开头的长字符串[citation：1][citation：3]。请立即将其复制并保存到本地的安全位置（例如密码管理器）。一旦关闭此页面，你将无法再看到该令牌值，只能删除它并重新生成。

应用到你的代码
你已经完成了之前的代码修改，现在只需将刚刚复制的令牌填入配置文件即可：

yaml
certmagic:

	api_token: "cfat_这里粘贴你刚刚生成的令牌"
	email: "你的管理员邮箱@example.com"
	domains:
	  - "dev.example.com"

提示：虽然我们用了“编辑区域 DNS”模板，但这个令牌给的是 DNS 写入权限，足以让 CertMagic 通过 DNS-01 挑战验证。如果你将来想仅限读取数据，可以参考模板选项里的只读（Read）权限[citation：4][citation：10]。
*/
func (app *Core) setupCertMagic() error {
	if !app.certMagicEnabled {
		return nil
	}

	// 配置 Cloudflare DNS 提供商
	dnsProvider := &cloudflare.Provider{
		APIToken: app.certMagicConfig.APIToken, // 只需要 API Token
	}

	// 创建证书存储目录
	if app.certMagicConfig.CacheDir != "" {
		// 创建存储目录
		os.MkdirAll(filepath.Dir(app.certMagicConfig.CacheDir), 0755)
	}

	// 配置 CertMagic 使用 DNS-01 挑战
	certmagic.DefaultACME.Agreed = true
	certmagic.DefaultACME.Email = app.certMagicConfig.Email
	// 使用生产环境（测试时可以先换成 staging）

	// 关键：设置 DNS 挑战的提供商
	certmagic.DefaultACME.DNS01Solver = &certmagic.DNS01Solver{
		DNSManager: certmagic.DNSManager{
			DNSProvider: dnsProvider,
		},
	}

	// 可选：禁用 HTTP-01 和 TLS-ALPN-01 挑战（只用 DNS-01）
	certmagic.DefaultACME.DisableHTTPChallenge = true
	certmagic.DefaultACME.DisableTLSALPNChallenge = true

	storage := &certmagic.FileStorage{Path: app.certMagicConfig.CacheDir}
	magic := certmagic.NewDefault()

	// 关键：使用 NewACMEIssuer 函数创建 ACMEIssuer [citation:9]
	acmeIssuer := certmagic.NewACMEIssuer(magic, certmagic.ACMEIssuer{
		CA:                      certmagic.LetsEncryptProductionCA,
		TestCA:                  certmagic.LetsEncryptStagingCA,
		Email:                   app.certMagicConfig.Email,
		Agreed:                  true,
		DisableHTTPChallenge:    true,
		DisableTLSALPNChallenge: true,
		DNS01Solver: &certmagic.DNS01Solver{
			DNSManager: certmagic.DNSManager{
				DNSProvider: dnsProvider,
			},
		},
	})
	if app.certMagicConfig.CacheDir != "" {
		magic.Storage = storage
	}

	// 测试环境用 Staging CA [citation:9]
	// if app.Debug {
	// 	acmeIssuer.CA = certmagic.LetsEncryptStagingCA
	// }
	// 关键：设置 Issuers 数组（不是 Issuer） [citation:2][citation:9]
	magic.Issuers = []certmagic.Issuer{acmeIssuer}

	magic.SubjectTransformer = func(ctx context.Context, domain string) string {
		primaryDomain := ExtractPrimaryDomain(domain)
		if strings.HasSuffix(domain, "."+primaryDomain) {
			return "*." + primaryDomain
		}
		return domain
	}

	// 6. 获取 TLS 配置
	ctx := context.Background()
	err := magic.ManageSync(ctx, app.certMagicConfig.Domains)
	if err != nil {
		return fmt.Errorf("failed to manage certificates: %w", err)
	}
	// 其他共享可调用
	app.Server.TLSConfig = magic.TLSConfig()

	Info("CertMagic enabled with Cloudflare DNS-01 challenge for domains: %v",
		app.certMagicConfig.Domains)

	return nil
}

// onDemand 获取证书
//
// email 管理员邮箱
// path 证书存储路径
//
// certmagic:
//
//	email: string
//	path: string
func (app *Core) onDemand(email, path string) error {
	// if app.Debug {
	// 	certmagic.DefaultACME.CA = certmagic.LetsEncryptStagingCA
	// } else {
	// 	certmagic.DefaultACME.CA = certmagic.LetsEncryptProductionCA
	// }

	// 配置 onDemand 获得证书
	certmagic.DefaultACME.Agreed = true // 同意 Let's Encrypt 的条款
	certmagic.DefaultACME.Email = email

	magic := certmagic.NewDefault()
	magic.OnDemand = &certmagic.OnDemandConfig{
		DecisionFunc: func(ctx context.Context, name string) error {
			return nil
		},
	}

	magic.SubjectTransformer = func(ctx context.Context, domain string) string {
		primaryDomain := ExtractPrimaryDomain(domain)
		if strings.HasSuffix(domain, "."+primaryDomain) {
			return "*." + primaryDomain
		}
		return domain
	}

	if path != "" {
		os.MkdirAll(filepath.Dir(path), 0755)
		magic.Storage = &certmagic.FileStorage{Path: path}
	}

	app.Server.TLSConfig = magic.TLSConfig()

	return nil
}
