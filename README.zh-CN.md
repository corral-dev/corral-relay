**语言：** [English](README.md) | 简体中文

<p align="center"><img src="docs/brand/corral-icon.png" alt="Corral" width="112" height="112"></p>
<h1 align="center">Corral 中继</h1>

离开开发机的局域网后，仍可从 iPhone 或 Mac 访问编程助手。在自己的服务器上运行中继；[Corral 开发机](https://github.com/corral-dev/corral)主动向外连接，无需开放开发机端口或为它配置公网 IP。

会话通信在开发机与 [Apple 客户端](https://github.com/corral-dev/corral-apple)之间采用端到端加密。中继可看到路由标识、连接时间和字节数。

**同一网络内可以不用中继。** 使用[局域网直连](#局域网直连)配对即可。跨网络访问需要一台服务器，以及配置了 TLS 的域名。

## 支持的平台

**macOS、Linux 和 Windows**，需要 Go 1.25+。Linux 服务与 TLS 反向代理示例位于 [`deploy/`](deploy/)。

## 安装

前置条件：**Go 1.25 或更新**（[go.dev/dl](https://go.dev/dl/)）。

```bash
go install github.com/corral-dev/corral-relay/cmd/corral-relay@latest
```

这条命令适用于 macOS、Linux 和 Windows PowerShell。若找不到 `corral-relay`，将 `go env GOPATH` 所指目录下的 `bin` 加入 `PATH`。目前尚无预编译二进制包。

<details>
<summary>安装脚本或源码构建</summary>

macOS 或 Linux 上也可用安装器从源码构建，安装到 `~/.local/bin`：

```bash
curl -fsSL https://raw.githubusercontent.com/corral-dev/corral-relay/main/install.sh | sh
```

从源码目录构建：

```bash
git clone https://github.com/corral-dev/corral-relay.git
cd corral-relay
go build -o corral-relay ./cmd/corral-relay
```

</details>

## 快速开始

先在本地启动一个中继。开发机登记保存在 JSON 文件中，无需配置数据库或账号。

**macOS / Linux：**

```bash
corral-relay -addr 127.0.0.1:8788 -node-id relay-1 \
  -state "${XDG_CONFIG_HOME:-$HOME/.config}/corral-relay/hosts.json"
```

**Windows PowerShell：**

```powershell
corral-relay -addr 127.0.0.1:8788 -node-id relay-1 -state "$env:USERPROFILE\.config\corral-relay\hosts.json"
```

保持进程运行，在另一个终端检查：

```bash
curl http://127.0.0.1:8788/healthz
# {"ok":true}
```

Windows 上使用 `Invoke-RestMethod http://127.0.0.1:8788/healthz`。每个中继实例应使用独立且稳定的 `-node-id`。

跨网络使用时，先按[部署](#部署)说明在中继前配置 TLS，再在 Corral 开发机上运行：

```bash
corral remote on --relay-url wss://relay.example.com
corral remote pair
```

在 Apple 客户端中扫描或粘贴配对码。配对码**一次性使用，十分钟后过期**，可授权访问开发机。请勿放入录屏、日志或公开报告。

## 局域网直连

同一局域网或私有组网中，Apple 客户端可直连开发机。在开发机上运行：

```bash
corral remote on --no-relay
corral remote pair
```

## 中继存了什么

单租户：一份 JSON 文件，每台开发机一行（路由标识、显示名、Ed25519 公钥、
节点、时间戳）。多租户：Postgres 里再加账号、设备码和用量计数。**会话内容
一律不存。** 登记会一直保留，直到操作者手动删除；没有自动过期。开发机丢了
旧注册密钥，只能由操作者删除中继上的旧登记——新密钥永远不能悄悄抢走一台离
线开发机的坑位。

## 多租户（可选）

<details>
<summary>账号与配额配置</summary>

若需要账号和配额，配置 Postgres 与 GitHub OAuth 应用，并将 `CORRAL_PUBLIC_URL` 设置为部署所用的 HTTPS 源站。

```bash
CORRAL_RELAY_MULTI_TENANT=1 \
CORRAL_DATABASE_URL=postgres://corral:PASSWORD@db-host/corral_relay \
CORRAL_PUBLIC_URL=https://relay.example.com \
CORRAL_GITHUB_CLIENT_ID=... \
CORRAL_GITHUB_CLIENT_SECRET=... \
CORRAL_SESSION_SECRET=... \
corral-relay --multi-tenant
```

每台开发机只登记到一个已登录账号；其他账号不能登记同一台开发机的路由标识，
开发机数和流量配额也按账号分别核算。客户端的数据连接不再携带账号登录：一次性
配对码与双方确认过的设备密钥，只授权这个客户端连接指定开发机——换网重连不依赖
账号会话。

连接资源的边界：为了让已配对客户端不带账号会话也能换网重连，路由标识必须
可被访问，因此中继会先按路由标识接入一条受限速的连接，再由开发机验证设备发
来的加密握手。未配对的调用方读不到、控制不了开发机，但仍可能消耗有限的建连
资源。要把中继开放给不受信任的大规模用户，应当补上短时、绑定开发机的连接凭
据（尚未实现），并单独公布路由与用量元数据的保留和删除规则。

</details>

## 推送通知（可选）

<details>
<summary>为自行构建的 iPhone 应用配置 APNs</summary>

若需要 iPhone 通知，配置与客户端应用标识匹配的 APNs 凭据。未配置 APNs 时，应用打开期间仍会更新会话。

如果你确实有苹果开发者账号和自己的打包，这样设置（以下均为占位值）：

```bash
CORRAL_PUSH_KEY=/path/to/AuthKey.p8
CORRAL_PUSH_KEY_ID=PLACEHOLDER_KEY_ID
CORRAL_PUSH_TEAM_ID=PLACEHOLDER_TEAM_ID
CORRAL_PUSH_BUNDLE_ID=com.example.corral
```

中继转发的通知正文本身也是密文，由手机在本地解开后再展示，中继看不到标题
和正文。APNs 返回 `200` 只表示**已接受投递，不等于已送达手机**。

</details>

## 部署

前面放一层反向代理终止 TLS（Caddy、nginx 或同类），再让配套命令行连
`wss://your-own-domain`。**唯一必须配对的地方是关掉代理的读写超时**：开发机那
条出站连接会几小时一动不动，超时设短了会被悄悄掐断，表现为「客户端看着在线，
实际早断了」。现成的 Caddy 配置和 systemd 单元在 [`deploy/`](deploy/)；
管理用的 `/metrics` 监听默认只绑本机。

## 协议与验证

线协议（v2 帧、开发机鉴权、配对链接、端到端加密）见
[`docs/PROTOCOL_V2.md`](docs/PROTOCOL_V2.md)。
`scripts/device_probe.py` 可验证连接、握手、配对及会话列表：

```bash
python3 scripts/device_probe.py --relay wss://relay.example.com \
    --key <host-public-key-hex> --code <pairing-code>
```

## 获取帮助与参与贡献

[报告问题或提出功能建议](https://github.com/corral-dev/corral-relay/issues/new/choose)。公开报告前请去除密钥、配对码及私有开发机信息。漏洞请使用[私密安全报告](https://github.com/corral-dev/corral-relay/security/advisories/new)。


见 [CONTRIBUTING.md](CONTRIBUTING.md)。外部贡献需要在 Pull Request 里同意
[贡献者许可协议](CLA.md)。`Corral` 名称与 artwork 另有
[品牌政策](BRAND_POLICY.md)。

## 许可

[GNU Affero 通用公共许可证 3.0 版，仅该版本](LICENSE)。如果你把修改版当作
网络服务运行，必须向该服务的用户提供其源码。
