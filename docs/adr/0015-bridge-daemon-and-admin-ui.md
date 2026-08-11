# ADR 0015: Bridge daemon and local admin web UI

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-11
- **Scope of Work reference:** Phase 1 (`bridge/` local agent: scanning,
  transfers, policy configuration), §5.4 (management application), Phase 0
  go/stop rule

## Context

By this point in the project, the RomM-facing pipeline was real and proven
against a live server: capability probing, scanning, canonicalization,
verification, chunked upload (`bridge/ingest.RommUploader`), and ingestion
reconciliation (`bridge/ingest.RommLibrary`) all worked, exercised by a
one-shot CLI (`cmd/swarm-bridge`) that read `ROMM_URL`/`ROMM_TOKEN` from the
environment and exited after one action.

That is real evidence, but it is not a product. The project owner's own
words: despite all the proven plumbing, there was still nothing they could
actually deploy and use — no persistent process, no config storage, no way
to manage a RomM connection or a credential except by re-exporting
environment variables before every command, and no Docker packaging at all,
despite Scope of Work's own framing of the Bridge as software that "runs on
other people's Docker and Unraid hosts."

This record exists because saying yes to that request means writing the
first real code under Phase 1's `bridge/` row, and the project's own
go/stop rule (`docs/phase0/acceptance-evidence.md`) says Phase 1 has not
formally been unblocked — one gate (a CGNAT hardware proof, ADR 0002) is
still open. That should be a conscious decision, not something that happens
by accident because a feature request showed up.

## Decision

**Build it, as a scope that never touches cross-Bridge federation.**

The go/stop gate that remains open is specifically about *cross-Bridge*
connectivity — one Bridge serving another Bridge's transfer request through
a Swarm, a Host, and a relay (`bridge/transport/`, `relay/`, `host/`). A
persistent daemon, a local config store, a local admin web UI, and a
Docker image that wrap the already-proven **RomM API-only pipeline** —
where the only two parties are the Bridge and the operator's own RomM
server — never exercises that gate at all. It is a self-contained slice:

- A persistent Bridge config (RomM URL and token, destination mode, staging
  paths, an admin password) replacing environment-variable-per-invocation.
- A persistent transfer journal replacing the CLI's in-memory one.
- A long-running process (`cmd/bridge`) instead of a one-shot CLI.
- A local web UI over that process: connection settings, library browsing,
  triggering and observing an import, pulling a copy of an item back out of
  the operator's own RomM.
- Docker packaging for the above.

None of it claims Phase 1 is unblocked, and none of it is cross-Bridge
federation. `bridge/transport/`, `relay/`, and `host/` are untouched.
`web/` (the future cross-Swarm member portal, ADR 0013, Phase 5) is a
different thing entirely and is also untouched — the two must not be
conflated: this is a **local, single-instance admin UI**, not a client of a
Network Host that does not exist yet.

### Resolved sub-decisions

Four design questions came up building this that were worth deciding
explicitly rather than silently:

1. **What "downloads" means here.** There is no peer-to-peer Bridge-to-Bridge
   download yet — that needs the Host/Swarm/grant model, none of which
   exists. "Downloads" in this UI means pulling a copy of an item back out
   of the operator's *own* RomM library to local disk
   (`bridge/scan.RommSource.Download` already does this), plus an
   activity/history view of past imports. The UI must not imply peer
   downloads work.
2. **How a file gets into the Bridge to be imported.** Both an "inbox"
   directory the UI browses server-side (avoids double-transferring a large
   ROM through the browser) and a plain browser upload form, for
   convenience.
3. **Admin password hashing.** Hand-rolled PBKDF2-HMAC-SHA256
   (`crypto/hmac` + `crypto/sha256`, RFC 2898) rather than adding
   `golang.org/x/crypto` for bcrypt. This keeps the project's zero-Go-
   dependency streak intact — deliberate, not an oversight: PBKDF2 is a
   well-defined standard construction, not novel cryptography, and the
   project has consistently preferred stdlib-only until Phase 1 persistence
   specifically required otherwise (`go.mod`'s own comment already
   anticipated this moment).
4. **Docker image base.** Distroless, non-root, API-only-mode-first.
   Filesystem-publication mode still works in the container but without the
   smooth PUID/PGID remapping self-hosted apps conventionally offer for a
   mounted library volume, since a distroless image has no shell to do that
   remapping with. API-only mode never touches a library mount, so is
   unaffected. A shell-based (alpine + entrypoint script) image remains an
   option later if filesystem-publication-in-Docker turns out to matter more
   than expected.

## Options considered

### Build the daemon/UI/Docker slice now, scoped to API-only — chosen

Ships something real without touching the still-open go/stop gate. Cost:
Phase 1 code exists before Phase 1 is formally unblocked, which this record
makes visible rather than implicit.

### Wait for the CGNAT hardware proof before writing any `bridge/` code

Keeps the go/stop rule's letter intact. Rejected: the gate is about
cross-Bridge federation specifically: nothing about proving direct-plus-relay
connectivity is made easier or harder by whether a config file and a web UI
exist for the RomM-only path. Waiting would have meant continuing to ask the
project owner to re-export environment variables before every command
indefinitely, for no correctness benefit.

## Consequences

- `bridge/bridgeconfig/`, `bridge/ingest.FileJournal` (`bridge/ingest/filejournal.go`),
  `bridge/romm.Connect` (`bridge/romm/connect.go`), `bridge/adminui/`, and
  `cmd/bridge/` are new. `cmd/swarm-bridge` is unchanged in behavior except
  that its `connect()` becomes a thin wrapper over the new
  `bridge/romm.Connect`.
- A `Dockerfile` and `docker-compose.yml` exist at the repo root; `make
  docker-build`/`make docker-run` exist; CI builds (not pushes) the image.
- The Phase 0 go/stop table in `docs/phase0/acceptance-evidence.md` is
  unaffected by this work — it still correctly reports B2 (CGNAT hardware
  proof) as the sole remaining gate. This ADR's existence is what keeps that
  true rather than quietly stale.
- The RomM API token is now held by a long-running process instead of a
  short-lived CLI invocation, and is reachable through a web UI. The admin
  password and the distroless/non-root packaging choice above are the
  concrete responses to that increased exposure window.

## Open questions

- Whether filesystem-publication mode needs first-class Docker support
  (PUID/PGID remapping) badly enough to justify a heavier, shell-based image
  later — deferred until it's actually requested.
- Everything cross-Bridge (transport, relay, Host) remains exactly as open
  as ADR 0002 and ADR 0012 already record; this ADR does not touch or
  narrow those.
