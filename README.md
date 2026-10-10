**Languages:** English | [简体中文](README.zh-CN.md)

# corral-relay

<p align="center">
  <img src="docs/brand/corral-icon.png" alt="Corral" width="112" height="112">
</p>

Self-hosted encrypted relay that connects the Corral phone app to your
development machine: the machine dials **out** and holds the connection open,
the phone connects to the same relay, and the relay splices the two together.
**No open ports, no public IP, no VPN on your machine.**

The relay is a dumb pipe. Everything flowing through it is end-to-end encrypted
between your development machine and your phone — the relay only ever sees the
routing identity, connection timing, and byte counts it needs to route traffic.
It cannot read your code, prompts, or sessions, even if it is compromised.

Do I need this? **Only if the phone must reach the machine away from your
local network** (cellular, another Wi-Fi, travel). On the same network the
phone talks to the machine directly and you can skip the relay entirely (see
[LAN direct](#lan-direct)). There is **no shared public relay**: you run your
own, and neither the CLI nor the phone app ships with anyone else's relay
address built in.

## Supported platforms

The relay is pure Go with no cgo dependencies. It builds and runs on
**macOS, Linux, and Windows**; the Windows cross-compile
(`GOOS=windows go build ./...`) is verified locally — this repository
currently has no CI workflow. Service deployment files in [`deploy/`](deploy/) (systemd unit, Caddy example)
target **Linux** servers. The default state path below is a Unix convention —
on other systems point `--state` / `CORRAL_RELAY_STATE` at a writable path.

## Install

Prerequisite: **Go 1.25 or newer** ([go.dev/dl](https://go.dev/dl/)).

```bash
go install github.com/corral-dev/corral-relay/cmd/corral-relay@latest
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`. No prebuilt binaries are
published yet; installing from source is currently the only route.

From a clone, or to pin a version:

```bash
git clone https://github.com/corral-dev/corral-relay.git
cd corral-relay
go build -o corral-relay ./cmd/corral-relay
```

A `curl | sh` installer is also provided (needs only `sh`, `git`, and `go`).
It installs user-local to `$HOME/.local/bin` by default (no sudo); for a
system-wide install pass an explicit prefix:

```bash
curl -fsSL https://raw.githubusercontent.com/corral-dev/corral-relay/main/install.sh | sh
# system-wide instead: curl -fsSL ... | PREFIX=/usr/local sh
```

## Quick start

Single-tenant (default — no accounts, one JSON file). This starts a relay on
loopback and probes it:

```bash
CORRAL_RELAY_ADDR=127.0.0.1:8788 \
CORRAL_RELAY_STATE="${XDG_CONFIG_HOME:-$HOME/.config}/corral-relay/hosts.json" \
corral-relay
```

```bash
curl http://127.0.0.1:8788/healthz
# {"ok":true}
```

Point the CLI at **your** relay address (TLS termination in front is required
outside loopback — see [Deployment](#deployment)):

```bash
corral remote on --relay-url wss://relay.example.com
corral remote pair
```

The pairing code is **one-time and expires after ten minutes**. The pairing QR
is a root credential: keep it short-lived, never paste it into recordings,
logs, or shared sessions.

## LAN direct

On the same Wi-Fi or private network (including Tailscale-style meshes) you can
leave the relay off. The phone races a local WebSocket against any configured
relay and uses whichever connects first:

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

For operators who want accounts and quotas on **their own** deployment. Set
`CORRAL_PUBLIC_URL` to **your** HTTPS origin; do not copy anyone else's
hostname.

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
per account. Phone data-plane connections carry no account login: the one-time
pairing code plus the mutually confirmed device keys authorize a phone to one
specific host, which keeps reconnects working without an account session.

Known boundary, stated honestly: to let paired phones reconnect account-free,
the routing identity must stay reachable, so the relay accepts a rate-limited
connection first and the machine verifies the encrypted handshake. Unpaired
callers cannot read or control a machine but can still consume limited
connection resources. Deployments open to untrusted users at scale should add
short-lived per-host connection credentials (not yet implemented) and publish
their own metadata retention rules.

## Push notifications (optional)

Push only matters if you also build your own phone app: Apple's push
credentials are bound to an app identity, so a self-hosted relay cannot send
notifications to store builds. Without push the app still updates in real time
while open; you just don't get a system notification when an assistant starts
waiting for you.

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

## Deployment

Put TLS in front (Caddy, nginx, or equivalent) and hand the CLI a
`wss://your-own-domain` URL. The one setting you must get right is **disabling
the proxy's read/write timeouts**: the machine's outbound connection can sit
idle for hours, and short timeouts silently sever it (phone shows online,
traffic is dead). Ready-made examples live in [`deploy/`](deploy/); the admin
`/metrics` listener binds to loopback by default.

## Protocol and verification

The wire protocol (v2 frames, host auth, pairing links, end-to-end encryption)
is specified in [`docs/PROTOCOL_V2.md`](docs/PROTOCOL_V2.md).
`scripts/device_probe.py` plays the role of a phone — connect, handshake,
pair, list sessions — and doubles as a runnable reference implementation:

```bash
python3 scripts/device_probe.py --relay wss://relay.example.com \
    --key <host-public-key-hex> --code <pairing-code>
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Outside contributions require agreeing
to the [Contributor License Agreement](CLA.md) in the pull request. The
`Corral` name and artwork are covered by a separate [brand policy](BRAND_POLICY.md).

## License

[GNU Affero General Public License v3.0 only](LICENSE). If you run a modified
version as a network service, you must offer its source to that service's
users.
