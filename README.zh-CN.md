**语言：** [English](README.md) | 简体中文

# corral-relay

<p align="center">
  <img src="docs/brand/corral-icon.png" alt="Corral" width="112" height="112">
</p>

自建的加密中继，把手机版 Corral 和你的开发机接起来：开发机主动**往外**连中继
并保持连接，手机也连到同一个中继，中继把两边接起来。**不用开端口、不用公网
IP、开发机上也不用装 VPN。**

中继是一根哑管子。经过它的所有内容都在开发机与手机之间做了端到端加密，它只
看得到转发所需的路由标识、连接时间与字节数。它读不到你的代码、提示词和会话
内容——想读也读不了，被攻破了也一样。

需要自己搭吗？**只有手机离开局域网还要连开发机时才需要**（蜂窝、别的 Wi-Fi、
出差）。同一网络下手机直连开发机，可以完全不用中继（见[局域网直连](#局域网直连)）。
**没有共享的公共中继**：中继要自己跑，命令行和手机应用都不会内置任何别人的
中继地址。

## 支持的平台

中继是纯 Go，没有 cgo 依赖，可以在 **macOS、Linux、Windows** 上构建运行；
Windows 交叉编译（`GOOS=windows go build ./...`）已在本地验证——本仓库目前
没有 CI 流水线。[`deploy/`](deploy/) 下的服务部署文件
（systemd 单元、Caddy 示例）面向 **Linux** 服务器。默认状态文件路径是 Unix
惯例——其他系统上把 `--state` / `CORRAL_RELAY_STATE` 指到可写路径即可。

## 安装

前置条件：**Go 1.25 或更新**（[go.dev/dl](https://go.dev/dl/)）。

```bash
go install github.com/corral-dev/corral-relay/cmd/corral-relay@latest
```

确保 `$(go env GOPATH)/bin` 在 `PATH` 里。目前还没有发布预编译二进制包，
从源码安装是唯一的安装途径。

从克隆安装，或要锁定版本：

```bash
git clone https://github.com/corral-dev/corral-relay.git
cd corral-relay
go build -o corral-relay ./cmd/corral-relay
```

也提供 `curl | sh` 安装脚本（只需要 `sh`、`git`、`go`）。默认装到用户目录
`$HOME/.local/bin`（不用 sudo）；要装到系统目录则显式传前缀：

```bash
curl -fsSL https://raw.githubusercontent.com/corral-dev/corral-relay/main/install.sh | sh
# 装到系统目录：curl -fsSL ... | PREFIX=/usr/local sh
```

## 快速开始

单租户（默认——无账号，一份 JSON）。在回环地址上起一个中继并验证：

```bash
CORRAL_RELAY_ADDR=127.0.0.1:8788 \
CORRAL_RELAY_STATE="${XDG_CONFIG_HOME:-$HOME/.config}/corral-relay/hosts.json" \
corral-relay
```

```bash
curl http://127.0.0.1:8788/healthz
# {"ok":true}
```

让命令行指向**你自己的**中继地址（回环之外必须在前面终止 TLS，见[部署](#部署)）：

```bash
corral remote on --relay-url wss://relay.example.com
corral remote pair
```

配对码是**一次性的，十分钟过期**。配对二维码属于根凭据：保持短时有效，
不要把它贴进录屏、日志或共享会话。

## 局域网直连

手机和开发机在同一个网络里——或者你已经有 Tailscale 这类私有组网——可以完全
跳过中继。手机会同时试本地 WebSocket 和已配置的中继，哪边先连上用哪边：

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

只给**自己**要开账号/配额的部署用。`CORRAL_PUBLIC_URL` 必须是你的 HTTPS 源站，
不要抄别人的主机名。

```bash
CORRAL_RELAY_MULTI_TENANT=1 \
CORRAL_DATABASE_URL=postgres://corral:密码@数据库主机/corral_relay \
CORRAL_PUBLIC_URL=https://relay.example.com \
CORRAL_GITHUB_CLIENT_ID=... \
CORRAL_GITHUB_CLIENT_SECRET=... \
CORRAL_SESSION_SECRET=... \
corral-relay --multi-tenant
```

每台开发机只登记到一个已登录账号；其他账号不能登记同一台开发机的路由标识，
开发机数和流量配额也按账号分别核算。手机的数据连接不再携带账号登录：一次性
配对码与双方确认过的设备密钥，只授权这部手机连接指定开发机——换网重连不依赖
账号会话。

如实说明的已知边界：为了让已配对手机不带账号会话也能换网重连，路由标识必须
可被访问，因此中继会先按路由标识接入一条受限速的连接，再由开发机验证设备发
来的加密握手。未配对的调用方读不到、控制不了开发机，但仍可能消耗有限的建连
资源。要把中继开放给不受信任的大规模用户，应当补上短时、绑定开发机的连接凭
据（尚未实现），并单独公布路由与用量元数据的保留和删除规则。

## 推送通知（可选）

推送只有在你同时自己打包手机应用时才有意义：苹果的推送凭据绑定应用标识，
自建中继没法给商店版本发通知。没有推送时应用打开着照样实时更新，只是助手
开始等你回答时不会有系统通知把你叫回来。

如果你确实有苹果开发者账号和自己的打包，这样设置（以下均为占位值）：

```bash
CORRAL_PUSH_KEY=/path/to/AuthKey.p8
CORRAL_PUSH_KEY_ID=占位密钥编号
CORRAL_PUSH_TEAM_ID=占位团队编号
CORRAL_PUSH_BUNDLE_ID=com.example.corral
```

中继转发的通知正文本身也是密文，由手机在本地解开后再展示，中继看不到标题
和正文。APNs 返回 `200` 只表示**已接受投递，不等于已送达手机**。

## 部署

前面放一层反向代理终止 TLS（Caddy、nginx 或同类），再让配套命令行连
`wss://你自己的域名`。**唯一必须配对的地方是关掉代理的读写超时**：开发机那
条出站连接会几小时一动不动，超时设短了会被悄悄掐断，表现为「手机上看着在线，
实际早断了」。现成的 Caddy 配置和 systemd 单元在 [`deploy/`](deploy/)；
管理用的 `/metrics` 监听默认只绑本机。

## 协议与验证

线协议（v2 帧、开发机鉴权、配对链接、端到端加密）见
[`docs/PROTOCOL_V2.md`](docs/PROTOCOL_V2.md)。
`scripts/device_probe.py` 会扮演一部手机——连接、握手、配对、拉一次会话列表，
既可以验证自建中继通不通，也是协议的一份可运行参考实现：

```bash
python3 scripts/device_probe.py --relay wss://relay.example.com \
    --key <开发机公钥十六进制> --code <配对码>
```

## 参与贡献

见 [CONTRIBUTING.md](CONTRIBUTING.md)。外部贡献需要在 Pull Request 里同意
[贡献者许可协议](CLA.md)。`Corral` 名称与 artwork 另有
[品牌政策](BRAND_POLICY.md)。

## 许可

[GNU Affero 通用公共许可证 3.0 版，仅该版本](LICENSE)。如果你把修改版当作
网络服务运行，必须向该服务的用户提供其源码。
