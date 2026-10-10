# Corral relay (public source)

Zero-knowledge relay + APNs: the development machine dials out, the phone connects in, ciphertext is forwarded end to end.

This repository is self-hosting-first. Anything about operating a shared public instance lives outside this repository; the public tree must never name maintainer servers.

## Documentation

- [`docs/PROTOCOL_V2.md`](docs/PROTOCOL_V2.md): read before changing, reviewing or debugging relay frame layout, routing identifiers, development-machine authentication, pairing links, end-to-end HKDF info, multi-tenant HTTP, quotas or node affinity. Skipping it mixes up v1 header fields, lets an offline key rotation steal a slot, or mistakes content encryption for metadata invisibility.
- [`README.md`](README.md) / [`README.zh-CN.md`](README.zh-CN.md): read before self-hosting / multi-tenant installation, runtime environment variables, reverse-proxy timeouts or push configuration.
- [Remote protocol](https://github.com/corral-dev/corral/blob/main/docs/REMOTE_KNOWLEDGE_BASE.md): required cross-repository reading when changing the phone-to-machine application protocol, the pairing security line, or debugging "someone took the public channel while the lid was closed".
- [`deploy/`](deploy/): read before changing Caddy / systemd deploy units (proxy read/write timeouts must stay off or idle dev-machine connections get cut).

## Hard constraints

- The relay is a dumb pipe: it sees version/type/channel and routing metadata only, never end-to-end payloads.
- Protocol strings, subprotocols, request headers and HKDF info always use `corral`; never introduce `pickup`.
- **Going offline never lets another registration key steal the slot**; rotation must present the old key as `Prev-Auth` (multi-tenant rejects TOFU/`Prev-Auth`).
- Shared-relay operations and overwrite-install failures do not belong in long-form docs here.

## Directory map

| Path | Purpose |
|---|---|
| cmd/corral-relay | Relay entry and multi-tenancy |
| internal/ | Relay implementation |
| docs/PROTOCOL_V2.md | Protocol v2 |
| deploy/ · scripts/ | Deployment and helpers |
| Cross-repository remote contract | [CLI remote knowledge base](https://github.com/corral-dev/corral/blob/main/docs/REMOTE_KNOWLEDGE_BASE.md) · [CLI `src/corral/remote/`](https://github.com/corral-dev/corral/tree/main/src/corral/remote) |

## Cross-component rules (shared with the CLI and Apple repositories)

- Session behavior changes belong to the owning layer: reusable history interpretation and session semantics live in the SessKit library consumed by the CLI; product behavior (launch/resume/handoff, ownership, transport, presentation) lives in Corral. Do not reinterpret raw history to bypass an upstream defect.
- Supported runtimes are an explicit allowlist (Claude Code, Codex, OpenCode, Cursor, Pi). Older compatibility shims stay dormant: kept in source, unmaintained, hidden from every user-visible entry.
- Defect reports are checked against all ends of the same user scenario in the same task; each end's verdict (fixed / not reproduced / no such feature) is recorded at delivery.
