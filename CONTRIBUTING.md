# Contributing to corral-relay

Thanks for considering a contribution. This relay is a zero-knowledge pipe:
it routes ciphertext between a development machine and a phone and never sees
session content. Keep every change consistent with that boundary.

## Scope

- This repository covers the relay only: `cmd/`, `internal/`, `deploy/`,
  `scripts/device_probe.py`, and these public docs.
- Before touching framing, auth, pairing, or push behavior, read
  [`docs/PROTOCOL_V2.md`](docs/PROTOCOL_V2.md). Protocol strings, subprotocols,
  headers, and HKDF info use `corral`; do not introduce new `pickup` names.
- Do not touch production operations: no maintainer servers, domains,
  credentials, device identifiers, or absolute home paths in code, docs,
  examples, or tests.

## Requirements

- Go 1.25 or newer. Pure Go, no cgo.
- Optional: Postgres (multi-tenant paths), Python 3 (the device probe),
  Caddy or equivalent (deployment examples).

## Checks

Run all three before opening a pull request; all must pass with no warnings:

```bash
go build ./...
go vet ./...
go test -race ./...
```

Postgres has no tests in this suite — state that explicitly if you change
`internal/store/postgres`. Known unresolved findings are listed at the end of
[`docs/PROTOCOL_V2.md`](docs/PROTOCOL_V2.md); check whether your change
overlaps them.

## Pull requests

- One focused change per PR, with a short description of behavior before and
  after, and the check output above.
- Keep English and Simplified Chinese READMEs aligned: same structure and
  commands, translate only the prose.
- Do not paste secrets, tokens, private keys, internal hostnames, or personal
  paths into the PR, issues, logs, or test fixtures. Use `example.com`
  placeholders and redacted values.
- By opening a PR you agree to the [Contributor License Agreement](CLA.md):
  state `I agree to the CLA in CLA.md` in the PR description. The maintainer
  never signs on your behalf.

## License and brand

Contributions are accepted under the repository license,
[AGPL-3.0-only](LICENSE); the license text itself stays verbatim. The `Corral`
name and artwork are not covered by the code license — see
[BRAND_POLICY.md](BRAND_POLICY.md) before naming or presenting a fork.

## Publication privacy verification

The fresh public history excludes private signing identifiers, maintainer service endpoints, private planning and original private Git history. A whole-tree credential scan found no secrets. Exact leak-gate fingerprints cover only individually reviewed synthetic LAN fixtures, protocol-format examples, documentation paths/anchors and shell expressions. No file-wide or credential-rule exemption was added. Recheck changed findings before extending the list.
