# ADR 0001: Implementation stack

- **Status:** Accepted
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 (coordinator language, Bridge language), §9, Phase 0 deliverable "Select the initial implementation stack"

## Context

Scope of Work §4 leaves three language decisions open: the coordinator's
framework, whether the Bridge extends the existing Python proof of concept or
becomes a Go service, and the web frontend framework. §9 offers a suggested
baseline without committing to it.

Two constraints do most of the deciding:

1. The Bridge is distributed software. It runs on other people's Docker and
   Unraid hosts, it is upgraded by people who did not write it, and Scope of Work
   Phase 1 requires it to install "without requiring a RomM administrator
   account". Every runtime dependency is a support burden borne by someone else.

2. Phase 0's go/stop rule turns on proving direct transport with encrypted relay
   fallback under CGNAT. §9 suggests evaluating tsnet with self-hosted Headscale.
   tsnet is a Go library that embeds a node in-process. From any other language
   it becomes a sidecar process, a second container, and a second failure mode —
   before the networking has even been proven to work.

## Decision

- **Bridge:** Go.
- **Network Host:** Go.
- **Relay:** Go, deployable separately from the Host.
- **Shared protocol types:** a Go package (`protocol/`) consumed by all three.
- **Primary database:** PostgreSQL, from Phase 2.
- **Frontend:** deferred to ADR 0013; it consumes the same role-scoped API as
  every other client, so it does not constrain this decision.

Phase 0 is additionally built with **no dependencies outside the Go standard
library**, so the Phase 0 evidence can be reproduced anywhere without a module
proxy. Dependencies arrive with Phase 1 persistence and Phase 2 Host work.

## Options considered

### Go for the Bridge and the Host — chosen

One language across Bridge, Host, and relay. A statically linked Bridge with no
interpreter and no virtualenv, which matters on Unraid. tsnet embeds directly,
so the Phase 0 networking proof is a library call rather than an orchestration
problem. Cost: the existing Python proof of concept is reference material rather
than a foundation.

### Python Bridge with a FastAPI Host

Reuses the proof of concept and whatever RomM API knowledge is already encoded in
it, which is the fastest route to first evidence. Costs: a heavier container to
distribute; the direct-plus-relay proof needs an external sidecar or a different
networking library, which makes the single most load-bearing Phase 0 gate harder
to prove; and the Bridge is the component most sensitive to distribution weight.

### Go Bridge with a FastAPI Host

Splits the difference: a small distributable Bridge, faster schema iteration on
the Host. Cost is two languages and two toolchains for a project whose Host and
Bridge share a large amount of protocol logic, and a shared-types story that has
to be generated rather than imported.

## Consequences

- The Python proof of concept is not extended. Its findings about RomM's API
  should be transferred into the capability table in
  `bridge/romm/capability.go`, which is where such knowledge now lives.
- `protocol/` is imported directly by every component; there is no code
  generation step and no schema drift between Host and Bridge.
- Phase 0 artefacts build and test with `go test ./...` and nothing else.
- If the networking proof in ADR 0002 selects something other than tsnet, this
  decision is unaffected — it was chosen for distribution weight as much as for
  embedding.

## Open questions

None for the server-side components. The frontend framework and desktop
packaging remain open in ADR 0013.
