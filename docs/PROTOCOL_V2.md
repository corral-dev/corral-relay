# Corral 中继协议 v2

CLI、iOS 与 `corral-relay` 共用这份约定。产品名是 **Corral**；协议字符串、子协议、请求头、HKDF info 一律用 `corral`，不再引入 `pickup`。

v2 是干净重写：新 CLI / 新 App **不读 v1**。公共中继的 `/v1` 路由保留一个发版周期，只给「旧 CLI + 旧 App」那对存量组合用。

## 传输

| | v2 |
|---|---|
| 开发机出站 | `wss://<relay>/v2/host` |
| 开发机附加 bulk | `wss://<relay>/v2/host/attach`（需 `host_lane_attach`） |
| 手机接入 | `wss://<relay>/v2/device?host=<routing_id>`（可选 `&lane=bulk`） |
| WebSocket 子协议 | `corral.v2`（握手必须声明；服务端不匹配就拒） |
| 局域网直连 | 同一套帧与子协议，手机连 `ws://<lan>:<port>`，不经中继 |

开发机主动出站，不需要公网 IP、开端口或 VPN。局域网直连只是更快的候选，不是唯一通路。

## 帧

```
[1 字节版本 = 0x02][1 字节类型][16 字节通道][载荷]
```

单帧上限 8 MiB。类型值与 v1 相同：

| 值 | 名字 | 方向 |
|---|---|---|
| `0x01` | DEVICE_OPEN | 中继 → 两端 |
| `0x02` | DEVICE_CLOSE | 中继 ↔ 开发机 |
| `0x03` | HELLO | 两端（明文握手材料） |
| `0x04` / `0x05` | PING / PONG | 两端 |
| `0x06` | PUSH | 开发机 → 中继（密文推送壳） |
| `0x07` | REGISTERED | 中继 → 开发机 |
| `0x08` | LANE_ATTACHED | 中继 → 开发机（附加通道附着成功） |
| `0x09` | PUSH_RECEIPT | 中继 → 开发机（推送路由回执，见下） |
| `0x10` | DATA | 两端（加密应用层 JSON） |

中继只看版本、类型与通道，不解析载荷（`REGISTERED` / `LANE_ATTACHED` /
`PUSH_RECEIPT` 的 JSON 元数据除外，三者不含会话内容）。

## 推送与回执（2026-09-30 可靠性修订）

`PUSH` 载荷（UTF-8 JSON，`payload` 为 base64 密文，中继不解密）：

```json
{"token":"<device token>","env":"production","payload":"<base64>","id":"<hex,可选>"}
```

`id` 为开发机生成的关联标识（可空；旧中继忽略未知字段）。
中继对每条 `PUSH` 回一条 `PUSH_RECEIPT`（interaction 面，零通道）：

```json
{"id":"<echo>","ok":true,"code":"ok","status":200,"reason":"","apns_id":"..."}
```

`code` 稳定词汇：`ok / no_push_config / bad_request / bad_token / rejected /
throttled / transport / internal`；`reason` 为 Apple 返回的 `reason` 原文
（如 `BadDeviceToken`），`status` 为 APNs HTTP 状态。APNs `200` 仅表示
**已接受投递（accepted），不等于已送达手机**。

可靠性规则：未配置推送必须回 `no_push_config`（禁止静默成功）；
推送类错误一律转成回执，**永不因此关闭开发机主连接**；
回执发送为 best-effort。旧开发机忽略 `0x09`，旧中继不发回执
（新开发机按超时重试，永不把无回执当成功）。

`REGISTERED` 载荷（UTF-8 JSON）示例：

```json
{"generation":1,"node":"local","capabilities":["host_lane_attach"]}
```

旧开发机可忽略载荷与未知 capability，继续单 socket 行为。

## 开发机附加通道（host lane attach）

手机侧已可拆成控制面 / 数据面两条设备 WebSocket；完整路由隔离还需要开发机对中继也挂上第二条 **bulk** 物理连接。主注册仍走 `/v2/host`（`RegisterHost` 仍会顶掉同身份旧主连接）。附加通道不得再次 `RegisterHost`。

| | |
|---|---|
| 附加出站 | `wss://<relay>/v2/host/attach` |
| 子协议 | `corral.v2` |
| 请求头 | `X-Corral-Lane-Attach`（必填）；`X-Corral-Host-Pub`（建议，须与已登记公钥一致） |

```
X-Corral-Lane-Attach: v2attach.<routing_id>.<node_id>.<generation>.<lane>.<unix_ts>.<nonce_b64url>.<sig_b64url>
```

`lane` 目前仅允许 `bulk`（`interaction` 不能附着，那是主注册）。`generation` 与 `node_id` 必须与当前在线主注册的 `REGISTERED` 载荷一致；代次或节点不符 → `409`。开发机离线 → `503`。

签名消息（精确字节，`\n` 为 LF）：

```
corral/relay/v2 host-lane-attach\n
<routing_id>\n
<node_id>\n
<generation 十进制>\n
<lane>\n
<unix_ts 十进制>\n
<nonce 原始 16 字节>
```

用与主注册相同的开发机 **Ed25519** 钥匙签。时钟偏移、nonce 重放规则与主断言相同（附着 nonce 与主注册 nonce 分命名空间）。

成功后中继发 `LANE_ATTACHED`：

```json
{"lane":"bulk","generation":1}
```

约束：

- 附加通道不能替换主注册，也不能成为独立开发机身份。
- 主注册断开会使该代次上所有附着通道失效。
- 设备接入可带可选查询参数 `?lane=bulk`；未声明时默认为 `interaction`。未附着 bulk 时，bulk 设备帧回落到主 socket（降级，不算完整隔离）。
- 中继对每个目的端使用有界队列（interaction / bulk 均 ≥ 单帧上限 8 MiB）。`FromHost` 只入队，不在慢设备 `Send` 上阻塞读循环。队列溢出只重置受影响目的通道，不踢掉其他手机；合法单帧不得因此关掉开发机主连接。端点需重新握手/游标恢复，禁止中途丢弃密文却继续计数。

能力协商：仅当 `REGISTERED.capabilities` 含 `host_lane_attach` 时，新 CLI 才打开附加 socket；旧中继无此字段则保持单连接。

## 路由标识

```
routing_id = lowercase( RFC4648-base32( SHA256("corral/relay/v2 route" || x25519_pub_32)[:16] ) )
             去掉 '=' padding
```

固定 26 个字符，字母为 `a-z` 与 `2-7`。手机从配对二维码的 `k`（开发机 X25519 公钥）自行算出，因此二维码不再带 `h`。

中继只见到这个哈希，枚举不出可寻址的开发机。

## 开发机鉴权（签名断言）

每次 `/v2/host` 升级请求带：

```
X-Corral-Auth: v2.<routing_id>.<unix_ts>.<nonce_b64url>.<sig_b64url>
X-Corral-Host-Name: <显示名，可选，最长 80>
X-Corral-Version: <CLI 版本，可选>
X-Corral-Bundle-Id: <iOS bundle，可选，用来选推送 profile>
X-Corral-Host-Pub: <Ed25519 公钥 hex，32 字节。单租户首次 TOFU 必带；已登记后以服务端存档为准>
```

`nonce` 16 字节、`sig` 64 字节，均为 **unpadded base64url**。

签名消息（精确字节，`\n` 为 LF）：

```
corral/relay/v2 host-auth\n
<routing_id>\n
<unix_ts 十进制>\n
<nonce 原始 16 字节>
```

用开发机 **Ed25519** 注册私钥签（与端到端用的 X25519 `identity.key` 分开，落在状态目录 `host.key`，0600）。

服务端：

- 时钟偏移超过 ±60 秒 → 拒
- 同一 `routing_id`+`nonce` 在 120 秒内重放 → 拒
- 公钥必须与已登记的一致

**单租户**（自建默认）：首次见到的 `routing_id` 按信任首次使用登记公钥。之后公钥必须与已登记的一致。**离线也不允许用另一把钥匙抢走坑位。** 轮换时本次升级请求额外带：

```
X-Corral-Prev-Auth: v2.<routing_id>.<unix_ts>.<nonce_b64url>.<旧钥匙签名>
```

`Prev-Auth` 必须与 `X-Corral-Auth` 使用同一 `routing_id` / 时间戳 / nonce；旧钥匙签 `Prev-Auth`，新钥匙签 `X-Corral-Auth`，`X-Corral-Host-Pub` 为新公钥。丢了旧钥匙只能由操作者删除中继上的登记。

**多租户**（公共实例）：必须先 `POST /v2/hosts` 用账号凭据登记，连接时只验签，不 TOFU，也不接受 `Prev-Auth` 换钥匙。

不再使用 Bearer `host_token`。轮换 = 换 Ed25519 密钥；`routing_id` 由 X25519 派生，已配对手机不必重扫。

## 配对链接

```
corral://pair?v=2&n=<name>&k=<x25519_b64url>&c=<code>&r=<relay_wss>&l=<lan,lan>
```

| 参数 | 含义 |
|---|---|
| `v` | 必须是 `2`。`v=1` 由新客户端明确拒绝，提示升级电脑端后重新出码 |
| `n` | 开发机显示名 |
| `k` | 开发机 X25519 长期公钥，unpadded base64url |
| `c` | 一次性配对码，十分钟过期 |
| `r` | 中继 `wss://…` |
| `l` | 局域网提示，逗号分隔的 `host:port` |

没有 `h`。私钥不进二维码。

## 端到端加密

算法不变：X25519 + HKDF-SHA256 + ChaCha20-Poly1305。HKDF info 升版，避免与 v1 密钥误互通：

```
corral/remote/v2 d2h
corral/remote/v2 h2d
corral/remote/v2 push
```

握手顺序与 v1 相同（设备先发长期公钥 ‖ 临时公钥，开发机回临时公钥）。

## HTTP（仅多租户）

| 方法 | 路径 | 作用 |
|---|---|---|
| `POST` | `/v2/auth/device` | 设备码流程开始 |
| `POST` | `/v2/auth/token` | 轮询换 CLI 凭据 |
| `POST` | `/v2/hosts` | 登记/更新开发机（`Authorization: Bearer <cli token>`） |
| `GET` | `/healthz` | 公开健康检查，不吐数量 |
| `GET` | `/metrics` | 仅 admin 监听（默认 `127.0.0.1`） |

Web 控制台：`/login`（GitHub OAuth）、`/oauth/github/callback`、`/device`（输码授权）、`/account`（开发机与配额）。

## 配额（多租户）

超限回 HTTP `429` 或拒绝新通道，**不断已有会话**。

默认：每账号 5 台并发开发机、每台 16 路设备通道、每月 50 GiB 转发、每天 500 条推送。

## 节点亲和

`hosts.node_id` 记录开发机当前挂在哪个实例。手机打到别的节点时先回 `503`，响应头 `X-Corral-Node: <node_id>`。节点间转帧尚未实现。

## Open implementation findings (2026-10-05 review)

These findings describe unresolved implementation defects, not changes to the
protocol requirements above. The review covered the relay, not the CLI remote
daemon or Apple clients. Existing `go test -race ./...` passed; PostgreSQL has no
tests in that suite. Four additional checks in an isolated source copy reproduced
the connection and persistence defects below without changing production code.

| Priority | Finding and impact | Evidence / repair direction |
|---|---|---|
| P1 | Device connection binds to two different host generations during reconnect. `handleDevice` looks up the host before upgrade, but `AttachDeviceLane` looks it up again after upgrade. Reads and deferred cleanup retain the first pointer. A replacement between these operations drops HELLO/DATA and leaks the new host's peer slot even after the device closes. | Reproduced through a real local WebSocket, replacing the host at HTTP hijack. Return the actual attached host with the channel, or require attachment to the previously observed generation. See `internal/server/server.go`, `handleDevice`, and `internal/hub/hub.go`, `AttachDeviceLane`. |
| P1 | Concurrent host replacement can leave a live, unreachable registration. `RegisterHost` releases the hub lock while shutting down the old host, then overwrites the map without checking for an intervening registration. Its socket and devices remain open outside the routing map. | Reproduced with two simultaneous replacements and an attached device on the intervening host. Publish replacement ownership atomically and close exactly the displaced generation outside the map lock. See `internal/hub/hub.go`, `RegisterHost`. |
| P1 | Approved account device codes are not consumed atomically. Two requests can both read the same approved code; DELETE success/affected rows are ignored, and both can issue permanent CLI tokens. A failed DELETE also permits sequential reuse. | Source-confirmed; no PostgreSQL concurrency run was performed. Consume and mint in one transaction with conditional deletion/locking and checked results. See `internal/store/postgres/postgres.go`, `ConsumeDeviceCode`. [PostgreSQL RETURNING](https://www.postgresql.org/docs/current/dml-returning.html) permits returning the actual deleted row. |
| P1 | Daily push quota is unenforced. `forwardPush` calls the sender directly; no production caller invokes `AddPushes`, and no send-path check reads `DailyPushes` or `PushesToday`. Accounts can exceed the documented 500/day limit while the console reports zero usage. | Source-confirmed across the relay. Reserve account push allowance atomically before sending and return an honest throttled receipt; do not close the host socket. See `internal/hub/hub.go`, `forwardPush`, and `internal/store/postgres/postgres.go`, `AddPushes`. |
| P2 | Failed single-tenant persistence is not retried. `Flush` clears `dirty` before directory creation/write/rename succeeds. After a transient failure, a later flush can return nil while the registration file is still absent. Restart can lose pinned registration keys and permit fresh TOFU. | Reproduced by blocking the state parent with a file, failing flush, removing the obstruction, and flushing again. Preserve pending dirty state on failure and serialize concurrent flushes. See `internal/store/file/file.go`, `Flush`. |
| P2 | v2 does not enforce the required WebSocket subprotocol. Listing `corral.v2` in `AcceptOptions` negotiates it but does not reject an empty negotiated protocol. All three v2 entry points omit the check. | Reproduced with a real device WebSocket that omitted subprotocols and still received DEVICE_OPEN. Check the offered/negotiated protocol before host registration/device attachment. [Pinned library documentation](https://pkg.go.dev/github.com/coder/websocket@v1.8.15#AcceptOptions) explicitly documents acceptance of the empty protocol. |
| P2 | Connection rate-limit entries have no expiry eviction. Unauthenticated requests with distinct arbitrary host query values create permanent `host:` map entries, including for offline/nonexistent hosts; expiry resets an accessed key but never removes it. | Source-confirmed. Expire unused entries, bound bucket cardinality, and validate routing identifiers. See `internal/server/server.go`, `allowBudget` / `allowDeviceConnect`. |

The PostgreSQL token race and push quota findings are not live-database or APNs
acceptance results. No claim is made about exploitation on a deployed relay.
The unauthenticated device data plane itself remains an approved product boundary
described in the remote knowledge base; it is not counted as a new defect here.

<!-- 该文档整理/压缩于 2026-09-05 -->
