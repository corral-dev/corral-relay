**Languages:** English | [简体中文](README.zh-CN.md)

<p align="center"><img src="docs/brand/corral-icon.png" alt="Corral" width="112" height="112"></p>
<h1 align="center">Corral Relay</h1>

Reach your coding agents from iPhone or Mac when you leave the host's local network. Run this relay on your own server; your [Corral host](https://github.com/corral-dev/corral) connects outward, so you do not need to expose the host's ports or give it a public IP.

Session traffic is end-to-end encrypted between the host and [Apple client](https://github.com/corral-dev/corral-apple). The relay sees routing identities, connection timing and byte counts.

**On the same network, you can skip the relay.** Pair directly with the host using [LAN access](#lan-direct). For access across networks, you need a server and a domain with TLS.

## Supported platforms

**macOS, Linux and Windows**, with Go 1.25+. Linux service and TLS-proxy examples are in [`deploy/`](deploy/).

## Install

Prerequisite: **Go 1.25 or newer** ([go.dev/dl](https://go.dev/dl/)).

```bash
go install github.com/corral-dev/corral-relay/cmd/corral-relay@latest
```

The command works on macOS, Linux and Windows PowerShell. Add the `bin` directory under `go env GOPATH` to your `PATH` if `corral-relay` is not found. Prebuilt binaries are not available yet.

<details>
<summary>Install script or source checkout</summary>

On macOS or Linux, this installer builds from source and installs into `~/.local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/corral-dev/corral-relay/main/install.sh | sh
```

To build from a checkout:

```bash
git clone https://github.com/corral-dev/corral-relay.git
cd corral-relay
go build -o corral-relay ./cmd/corral-relay
```

</details>

## Quick start

Start one relay locally. It stores host registrations in a JSON file; no database or account setup is needed.

**macOS / Linux:**

```bash
corral-relay -addr 127.0.0.1:8788 -node-id relay-1 \
  -state "${XDG_CONFIG_HOME:-$HOME/.config}/corral-relay/hosts.json"
```

**Windows PowerShell:**

```powershell
corral-relay -addr 127.0.0.1:8788 -node-id relay-1 -state "$env:USERPROFILE\.config\corral-relay\hosts.json"
```

Leave it running and check from another terminal:

```bash
curl http://127.0.0.1:8788/healthz
# {"ok":true}
```

On Windows, use `Invoke-RestMethod http://127.0.0.1:8788/healthz`. Give each relay instance its own stable `-node-id`.

To use the relay across networks, deploy it behind TLS using [Deployment](#deployment), then run on your Corral host:

```bash
corral remote on --relay-url wss://relay.example.com
corral remote pair
```

Scan or paste the code in the Apple client. The code is **one-time and expires after ten minutes**; it authorizes access to your host. Keep it out of recordings, logs and public reports.

## LAN direct

On the same local network or a private mesh, the Apple client can reach the host directly. Run on the host:

```bash
corral remote on --no-relay
corral remote pair
```

## What the relay stores

Single-tenant: one JSON file, one line per development machine (routing
identity, display name, Ed25519 public key, node, timestamps). Multi-tenant:
the same plus accounts, device codes, and usage counters in Postgres. **No
session content is ever stored.** Registrations persist until the operator
deletes them; there is no automatic expiry. If a machine loses its old
registration key, only the operator can clear the stale registration — a new
key can never silently take over an offline machine's slot.

## Multi-tenant (optional)

<details>
<summary>Account and quota configuration</summary>

To add accounts and quotas, configure Postgres and a GitHub OAuth application. Set `CORRAL_PUBLIC_URL` to your deployment's HTTPS origin.

```bash
CORRAL_RELAY_MULTI_TENANT=1 \
CORRAL_DATABASE_URL=postgres://corral:PASSWORD@db-host/corral_relay \
CORRAL_PUBLIC_URL=https://relay.example.com \
CORRAL_GITHUB_CLIENT_ID=... \
CORRAL_GITHUB_CLIENT_SECRET=... \
CORRAL_SESSION_SECRET=... \
corral-relay --multi-tenant
```

A host registers to exactly one authenticated account; another account cannot
claim that host's routing identity, and host and traffic quotas are accounted
per account. Client data-plane connections carry no account login: the one-time
pairing code plus the mutually confirmed device keys authorize a client to one
specific host, which keeps reconnects working without an account session.

Connection-resource boundary: to let paired clients reconnect account-free,
the routing identity must stay reachable, so the relay accepts a rate-limited
connection first and the machine verifies the encrypted handshake. Unpaired
callers cannot read or control a machine but can still consume limited
connection resources. Deployments open to untrusted users at scale should add
short-lived per-host connection credentials (not yet implemented) and publish
their own metadata retention rules.

</details>

## Push notifications (optional)

<details>
<summary>APNs configuration for your own iPhone build</summary>

To receive iPhone notifications, configure APNs credentials matching your client build. While the app is open, sessions update without APNs.

With your own Apple Developer account and build, set (values shown are
placeholders):

```bash
CORRAL_PUSH_KEY=/path/to/AuthKey.p8
CORRAL_PUSH_KEY_ID=PLACEHOLDER_KEY_ID
CORRAL_PUSH_TEAM_ID=PLACEHOLDER_TEAM_ID
CORRAL_PUSH_BUNDLE_ID=com.example.corral
```

Notification bodies forwarded by the relay are themselves ciphertext, decrypted
locally on the phone. The relay never sees titles or text. An APNs `200` means
**accepted for delivery, not delivered to the phone**.

</details>

## Deployment

Put TLS in front (Caddy, nginx, or equivalent) and hand the CLI a
`wss://your-own-domain` URL. The one setting you must get right is **disabling
the proxy's read/write timeouts**: the machine's outbound connection can sit
idle for hours, and short timeouts silently sever it (client shows online,
traffic is dead). Ready-made examples live in [`deploy/`](deploy/); the admin
`/metrics` listener binds to loopback by default.

## Protocol and verification

The wire protocol (v2 frames, host auth, pairing links, end-to-end encryption)
is specified in [`docs/PROTOCOL_V2.md`](docs/PROTOCOL_V2.md).
`scripts/device_probe.py` checks connection, handshake, pairing and session listing:

```bash
python3 scripts/device_probe.py --relay wss://relay.example.com \
    --key <host-public-key-hex> --code <pairing-code>
```

## Help and contribute

[Report a bug or request a feature](https://github.com/corral-dev/corral-relay/issues/new/choose). Remove keys, pairing codes and private host details from public reports. Use [private security reporting](https://github.com/corral-dev/corral-relay/security/advisories/new) for vulnerabilities.


See [CONTRIBUTING.md](CONTRIBUTING.md). Outside contributions require agreeing
to the [Contributor License Agreement](CLA.md) in the pull request. The
`Corral` name and artwork are covered by a separate [brand policy](BRAND_POLICY.md).

## License

[GNU Affero General Public License v3.0 only](LICENSE). If you run a modified
version as a network service, you must offer its source to that service's
users.
