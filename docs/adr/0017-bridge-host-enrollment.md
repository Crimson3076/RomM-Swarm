# ADR 0017: Bridge identity keys and Host enrollment over the network

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-11
- **Scope of Work reference:** Phase 2 follow-on, named explicitly in ADR
  0016's "explicitly out of scope": "Wiring a real `cmd/bridge` to call
  this Host's enrollment API over the network — needs Bridge-side
  identity-key generation and an HTTP client, which belong to a follow-on
  slice."

## Context

ADR 0016 built a real, tested Network Host identity/enrollment API —
`POST /api/bridges/enroll` and `POST /api/bridges/{id}/rotate` — and
proved it end to end, manually, over real HTTP, including in this session
against the project owner's own Docker + Postgres setup. It deliberately
left `cmd/bridge` unable to call any of it: no Bridge identity key scheme
exists anywhere in this codebase (ADR 0002's "what remains open" section
says so explicitly, and a repo-wide search confirms zero `ed25519` usage
anywhere), and `auth.Client.Rotate` — the Bridge-side half of the rotation
protocol, already implemented and tested in `auth/bridgestore.go` — has no
production implementation of the HTTP call its own doc comment says belongs
there.

This record exists because closing that gap is the first time a Bridge
process makes a real network call to anything other than the operator's own
RomM server. That's worth deciding deliberately, the same way ADR 0015 and
ADR 0016 each recorded their own scope before writing the code.

## Decision

**Build Bridge-side identity-key generation and a Host HTTP client now,
scoped to enrollment and credential rotation only.**

- An Ed25519 identity keypair, generated once on first join, never
  regenerated.
- A new persistent store (`bridge/swarmconn`) holding the Host URL and the
  identity private key — separate from both `bridgeconfig.Config` (local
  RomM/admin settings) and `auth.Credential`/`auth.FileStore` (the
  Host-enrollment refresh token, already Phase 0 and already tested).
- A new HTTP client package (`bridge/hostclient`) implementing enrollment
  and the exact `func(protocol.BridgeID, auth.Token) (auth.Result, error)`
  shape `auth.Client.Rotate` already expects.
- `cmd/bridge`'s `Daemon` gains the capability to join a Swarm and to
  manually test the connection — built and proven against a real,
  separately-running `cmd/host` process before any UI exists on top.

None of this touches cross-Bridge federation, inventory publication, or the
still-open CGNAT gate. It is exactly the two things ADR 0016 named as
missing, nothing more.

### Resolved sub-decisions

1. **Ed25519, stdlib `crypto/ed25519`, no new dependency.** The algorithm
   was genuinely undecided before this record — confirmed by grepping the
   whole repo and finding zero uses, and by ADR 0002 stating outright that
   no scheme exists yet. Ed25519 needs no new dependency (unlike Host's
   `pgx`/`argon2`, justified specifically because Host was never held to
   Bridge's zero-Go-dependency bar), and `protocol.BridgeIDFromPublicKey`
   already accepts a bare `[]byte` with no algorithm constraint to satisfy.
2. **No proof-of-possession today, and that's an inherited gap, not
   something this record introduces.** `POST /api/bridges/enroll` (built in
   ADR 0016, already shipped and tested) accepts a bare public key with
   nothing proving the caller holds the matching private key. Generating a
   real keypair Bridge-side doesn't change that the Host-side contract
   never asked for proof. Named plainly below rather than silently
   accepted or quietly "fixed" by inventing a new protocol version.
3. **A third persistent store, not a merge into an existing one — because
   `auth.Client.Store` is a concrete `*auth.FileStore` field, not an
   interface.** Read directly from `auth/bridgestore.go`: `Client{Store
   *FileStore; Rotate func(...) (Result, error)}`. Reusing `Client`
   verbatim (rather than modifying already-tested, transport-agnostic
   `auth/` code) means the refresh credential itself has to live in a real
   `auth.FileStore` file, at its own path. The Host URL and identity key
   don't fit that type at all, so they get their own new store,
   `bridge/swarmconn`, following `bridgeconfig.Config`'s own package doc
   comment, which already draws exactly this line: "This is deliberately a
   separate store from `auth.FileStore`/`auth.Credential`... The two happen
   to share a persistence recipe, not a type." A fourth hand-copy of the
   atomic-write recipe (`auth/bridgestore.go`, `bridge/bridgeconfig`,
   `bridge/ingest/filejournal.go`, now `bridge/swarmconn`) — still not
   worth factoring into a shared helper, the same call ADR 0015 made and
   left unactioned.
4. **The rotate wire response omits `generation`.** `hostapi`'s
   `handleRotateBridge` (ADR 0016) returns
   `{"refresh_token","access_expires_at","outcome"}` only — no generation
   field ever existed on that response. `bridge/hostclient`'s rotate
   implementation reconstructs it: read the previous generation from the
   credential file about to be overwritten, return `previous + 1`. This is
   exact, not a guess — `auth.Verifier.Rotate` always increments by exactly
   1 on both the normal and grace-recovery path (read directly from
   `auth/refresh.go`). Generation is diagnostic-only; nothing in `auth/`
   validates a Bridge-presented value, so an off-by-construction value here
   would never cause a protocol failure even if this reasoning were wrong.
5. **No automatic rotation ticker in this slice.** Nothing calls the Host
   on an ongoing basis yet — inventory publication is out of scope (ADR
   0016). Inventing a rotation schedule with no real consumer to justify it
   would be speculative. Instead: a manual "Test Swarm Connection" action
   (`Daemon.TestSwarmConnection`) that calls `auth.Client.Refresh()` once,
   mirroring the already-shipped `/api/connection/test` pattern for RomM
   connections in `bridge/adminui`. Automatic periodic rotation is a
   deliberate deferral, named below, for whenever something Host-facing
   that needs a continuously-live session actually exists.
6. **A `SWARM_HOST_URL`/`SWARM_INVITATION_CODE` env-var join path**,
   mirroring `Bootstrap`'s existing `ROMM_URL`/`ROMM_TOKEN` convenience
   exactly: seeds a join once, only if `bridge/swarmconn`'s store doesn't
   exist yet; never reconsulted on a later boot. This is what makes real
   two-process manual verification possible in this slice, before any UI
   exists to trigger a join by hand.

## Options considered

### Build identity-key generation and the HTTP client now, scoped to enrollment/rotation only — chosen

Closes exactly the two gaps ADR 0016 named, using entirely already-tested
building blocks (`auth.Client`, `auth.Verifier`'s wire contract) on both
ends. Cost: a Bridge process now makes an outbound network call to
something other than RomM for the first time, which is why this decision
is recorded rather than folded silently into a later slice.

### Wait for a Bridge-to-Bridge connectivity proof (the CGNAT gate) first

Rejected, for the identical reason ADR 0015 and ADR 0016 both gave: the
still-open gate is specifically about cross-Bridge federation over
direct-plus-relay transport. Enrolling with a Host over an ordinary HTTPS
call exercises none of that — a Host is not a peer Bridge, and this slice
never touches `bridge/transport/` or `relay/`.

### Add proof-of-possession to the enrollment protocol now, as part of this slice

Rejected as scope creep for this record: it would mean changing
`hostapi.handleEnrollBridge`'s already-shipped, already-tested contract
(ADR 0016), which is a Host-side decision this Bridge-side record
shouldn't make unilaterally. Named as an open question instead.

## Consequences

- `bridge/swarmconn/` and `bridge/hostclient/` are new. `cmd/bridge/swarm.go`
  is new, extending `Daemon` with private fields (`swarmStore`,
  `credentialStore`) and public accessor/action methods, following the
  existing field-privatization-for-interface-satisfaction pattern already
  used throughout `cmd/bridge/daemon.go`.
- `go.mod` gains no new dependencies — `crypto/ed25519` is stdlib. `go list
  -deps ./cmd/bridge` must stay exactly as free of `pgx`/`x/crypto`/any
  `host/...` package as it already is; this slice adds an HTTP client
  calling a Host, not a dependency on Host's own code.
- `cmd/bridge/main.go` gains one new call, `d.BootstrapSwarm(bootstrapCtx)`,
  immediately after the existing `d.Bootstrap(bootstrapCtx)` call, in the
  same non-fatal-error style.
- The Phase 0 go/stop table is unaffected, the same way it was unaffected
  by ADR 0015 and ADR 0016 — this record's existence is what keeps that
  true rather than quietly stale.

## Explicitly out of scope

- Host-side proof-of-possession for an enrolled public key — an inherited
  gap from ADR 0016's already-shipped contract, named here rather than
  fixed unilaterally.
- Automatic, periodic, or scheduled credential rotation — nothing yet
  depends on a continuously-live Bridge-to-Host session.
- Leaving one Swarm without a full, global credential revocation — the
  `auth` protocol as it exists only supports revoking a Bridge's entire
  credential family, across every Swarm it belongs to (ADR 0016, resolved
  sub-decision 3); this record doesn't change that.
- Any safety UI or warning around re-joining a *different* Host than the
  one already stored — mechanically possible (the store just gets
  overwritten), not guarded against yet.
- A Bridge admin UI page for any of this — that's ADR 0018's Step 9, built
  only after this record's daemon-side wiring is proven against a real
  Host, mirroring ADR 0015's own daemon-core-before-UI sequencing.

## Open questions

- Whether/when Host-side proof-of-possession should be added to the
  enrollment protocol, and what it should look like (a signed challenge is
  the obvious shape, given the keypair now exists) — a Host-side decision,
  deferred.
- When a real automatic rotation schedule becomes necessary, once
  inventory publication or any other continuously-live Bridge-to-Host
  activity exists.
- Whether re-joining a different Host should be blocked, warned, or require
  explicit confirmation once a UI exists for it (ADR 0018, Step 9).
