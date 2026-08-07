# RomM Swarm

A private, invitation-only federation of independently operated
[RomM](https://github.com/rommapp/romm) servers.

Local **Bridges** publish verified inventory to a central **Network Host**.
Members search a cached central index, compare it with their own library, request
missing items, and transfer approved content directly between Bridges. Neither
the Host nor the relay ever stores RomM passwords, RomM API tokens, or a durable
collection of ROM files.

This repository is at **Phase 0**: requirements freeze and technical proof. The
first milestone is tests and evidence for the Phase 0 acceptance criteria, not a
production interface. See
[docs/phase0/acceptance-evidence.md](docs/phase0/acceptance-evidence.md) for what
is proven, what is not, and why.

> **Phase 1 has not been unblocked.** Two of the three go/stop conditions are
> unmet: API-only import against a live RomM server, and a viable CGNAT
> connectivity path. Both need access this codebase cannot grant itself.

---

## Layout

Scope of Work §13.1 asks for `host`, `bridge`, `relay`, `web`, `protocol`, and
`docs` boundaries, beginning as modules in one repository.

| Path | What it is | Phase |
|---|---|---|
| `protocol/` | Versioned wire types: identifiers, Swarm-scoped aliases, digests, event vocabulary, manifests and deltas, the destination state machine | 0 |
| `verify/` | Format-aware canonicalization adapters and the four-identity model | 0 |
| `reference/` | Reference catalogue import, collection profiles, classification | 0 |
| `preservation/` | Risk states, independent replica counting, coverage | 0 |
| `auth/` | Rotating refresh credentials: grace-window recovery, reuse detection, transactional Bridge persistence | 0 |
| `bridge/romm/` | The RomM API adapter boundary and the capability probe | 0 |
| `bridge/scan/` | RomM listing, download, and analysis wired into a normalized local manifest | 0 |
| `bridge/destination/` | Staging, same-filesystem preflight, atomic no-overwrite publication | 0 |
| `bridge/publish/` | Per-Swarm sharing policies and filtered manifests | 0 |
| `bridge/ingest/` | RomM ingestion reconciliation and the receiving-flow driver | 0 |
| `bridge/` | Local agent: scanning, transfers, policy configuration | 1 |
| `host/` | Network Host: identity, invitations, index, grants | 2 |
| `relay/` | Encrypted fallback transport | 6 |
| `web/` | Member portal | 5 |
| `cmd/` | `swarm-probe`, `swarm-verify`, `swarm-fixtures` | 0 |
| `internal/` | Test doubles: synthesised ROM fixtures, a fake RomM server | 0 |
| `docs/adr/` | An architecture decision record per Phase 0 gate | 0 |
| `docs/phase0/` | Data map, privacy disclosure, retention schedule, threat model, filesystem-publication trust writeup, archive/ingestion behavior, evidence ledger | 0 |

Phase 0 has **no dependencies outside the Go standard library**, so its evidence
reproduces anywhere without a module proxy.

---

## Build and test

```sh
make            # gofmt check, vet, and the full test suite
make evidence   # only the Phase 0 acceptance tests, verbosely
make build      # the command-line tools
make demo       # generate a sample library and classify it
```

Requires Go 1.24.

`make demo` is the quickest way to see what the project does. It writes a sample
library — including one game present as both a plain and a copier image, a
trimmed cartridge dump, a zipped ROM, and a damaged dump — then classifies it
against a matching catalogue:

```
Reference: no-intro Nintendo - Game Boy 20260806-000000
Profile:   North America plus World 1G1R v1 (2 entries expected)

Smoke Quest (USA) (Rev 1).gb    verified_eligible   65536 bytes sha256:9cdd4276dbdd…
Smoke Quest (USA).gb            verified_excluded   65536 bytes sha256:9f143d83ba0d…
Smoke Quest (Japan).gb          verified_excluded   65536 bytes sha256:425364e3d935…
Zipped Quest (USA).zip          verified_eligible   65536 bytes sha256:fa0838f6931d…
Damaged Quest (USA).gb          unmatched           65536 bytes sha256:18e34e6309ad…
Smoke Sega (USA).bin            unmatched          131072 bytes sha256:24e1ad4d34c4…
Smoke Sega (USA).smd            unmatched          131072 bytes sha256:24e1ad4d34c4…
```

Read that output carefully: 1G1R picked the newest North American revision and
excluded the superseded and Japanese releases; the zipped holding verified
identically to a bare one; the damaged dump did not verify; and the two Sega
encodings — which share no bytes in common order — converged on one canonical
digest. They are `unmatched` only because the sample catalogue covers Game Boy.

---

## Tools

### `swarm-probe` — record what a RomM server can do

Reads the server's own OpenAPI document, matches it against the capabilities RomM
Swarm needs, and writes a fixture bundle that CI can replay without the server.

```sh
export ROMM_URL=https://romm.example
export ROMM_TOKEN=<scoped standard-user Client API Token>
make probe
```

The credential is read from the environment, never from a flag, so it stays out
of shell history and process listings. It never appears in the output; the tool
refuses to write a bundle that contains it. The server's address and any sample
library object are omitted unless explicitly requested, because bundles are meant
to be committed.

Exit code 2 means a required capability is missing — usable as a script gate.

**The capability table is currently an assumption.** It was written without
access to a live RomM instance. When a candidate path does not match, the bundle
prints every path the server does document, and the fix is one line in
`bridge/romm/capability.go`.

### `swarm-verify` — compute the four identities for files on disk

```sh
swarm-verify ~/roms/gb
swarm-verify -dat "Nintendo - Game Boy.dat" -platform gb -v ~/roms/gb
```

Reports the stored identity, the canonical identity, the adapter that produced
it, and — with a catalogue — the classification under the default collection
profile. It never writes to the files it reads.

### `swarm-fixtures` — generate a sample library and catalogue

Writes structurally valid cartridge images (real headers, real checksums, real
magic values) over deterministic filler, plus a matching Logiqx catalogue. No
copyrighted content is reproduced, and the same seed always produces the same
bytes — which is why the test suite carries no ROM data at all.

```sh
swarm-fixtures -out /tmp/sample
```

---

## The core idea, in one example

The same Mega Drive game circulates as a plain `.bin` and as a Super Magic Drive
`.smd`. The two files share no bytes in common order and hash to entirely
different values. They are the same game.

Hashing the stored file reports two unrelated holdings and inflates coverage.
So every file has **four** identities, kept separate:

| Identity | Used for |
|---|---|
| **Stored file** — bytes as they sit on disk | Provenance. Never reference matching. |
| **Container** — archive format and members | Provenance. Never matching; two zips of one ROM differ. |
| **Canonical payload** — after format-aware normalisation | Reference matching, transfer verification, the exact-file id |
| **Reference** — what the approved catalogue says a correct dump hashes to | The verification target |

Canonicalization opens archives, removes copier headers where the reference rules
define them, de-interleaves, and reconstructs trimmed dumps — **without ever
modifying the source file**. Every result names the adapter version that produced
it, so a rule change cannot silently reclassify history.

Only content that matches an approved reference entry and falls inside the active
collection profile is ever advertised, transferred, or counted.

See [ADR 0011](docs/adr/0011-verification-identity-model.md).

---

## Decisions

Scope of Work §4 is the source of truth for what is open.
[docs/adr/](docs/adr/README.md) holds the reasoning.

Accepted: the implementation stack (0001), canonical identifiers and
Swarm-scoped aliases (0009), the event vocabulary (0010), the verification model
(0011), refresh rotation recovery (0014).

Still open, and blocking: the networking stack (0002), supported RomM versions
(0003), and pilot legal risk acceptance (0008) — which needs a person, not a
commit.

Proposed and implemented as clearly named defaults, so the harness has something
concrete to test: the initial platform set (0004), the collection profile (0005),
central plaintext and retention (0006), browser delivery (0007), relay separation
(0012).

---

## What this project is not

From Scope of Work §8, the ones worth repeating here:

- Not a public or anonymous ROM search engine.
- Not central ROM storage. The Host holds an index; the relay buffers in flight.
- Not a reason to share RomM passwords or tokens with anyone.
- Not a claim that private membership or technical architecture is legal
  permission. *Architecture is mitigation, not permission* — Scope of Work §10.
