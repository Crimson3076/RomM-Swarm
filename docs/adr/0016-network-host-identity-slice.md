# ADR 0016: Network Host, slice 1 — identity, Swarms, invitations, Bridge enrollment

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-11
- **Scope of Work reference:** Phase 2 (`host/`: accounts, roles, Swarms,
  memberships, invitations, Bridge enrolment and revocation, Swarm-scoped
  aliases), §5.1, Phase 0 go/stop rule

## Context

By this point, the Bridge side of this project is real and proven: the
RomM-facing pipeline, and — most recently — a persistent daemon with a local
admin web UI (`cmd/bridge` + `bridge/adminui`, ADR 0015), Dockerized and
manually verified against a live RomM server. `host/` — the central
coordination service — remains exactly what it started as: a README
describing scope, zero implementation.

The project owner asked why the Bridge admin UI was built before the Host,
given the whole point of this system is Bridges federating through a Host.
The honest answer has two parts. First, there was no Host to connect to —
ADR 0015's work was never a choice between "wire the Bridge into the
existing Host" and "build it standalone"; Host didn't exist either way.
Second, and more importantly: Host's job is coordinating Bridge-to-Bridge
transfers over a connectivity model (direct QUIC plus relay fallback,
ADR 0002) that still has exactly one open go/stop gate — a real CGNAT
hardware proof this codebase cannot supply itself. Building the
transfer-coordination parts of Host now would mean building against
unproven assumptions about how that connectivity actually behaves.

But Host is not one monolithic thing. Its foundational layer — accounts,
Swarms, memberships, invitations, and Bridge enrollment/credential
rotation/revocation — has no dependency on that gate at all. It never
touches inventory, grants, or the relay. It is also substantially
de-risked already: `auth.Verifier`/`auth.Store` (Phase 0, already
implemented and tested in `auth/refresh.go`) is explicitly documented as
the Host-side half of the Bridge credential protocol, deliberately built
against an interface so a real backing store could be swapped in without
touching that package. This record exists because writing the first real
`host/` code, before the CGNAT gate closes, should be a conscious decision
made visible — the same reason ADR 0015 exists for `bridge/`.

## Decision

**Build the identity/Swarm/membership/invitation/enrollment slice now,
scoped to never touch cross-Bridge federation, inventory, or anything ADR
0006 (central plaintext and retention, still Proposed) has not settled.**

- A PostgreSQL-backed schema and connection layer (`host/hoststore`) —
  ADR 0001 already settled PostgreSQL as Host's database "from Phase 2";
  this is the first time that decision is implemented, not a new one.
- A domain layer (`host/directory`) composing that store with
  `auth.Verifier`: account bootstrap and login, Swarm creation, invitation
  issuance and redemption, and Bridge enrollment, credential rotation,
  revocation, and owner-triggered re-enrollment.
- A minimal JSON HTTP API (`host/hostapi`) — no HTML UI. Per ADR 0013
  (still Open, deferred to Phase 5), the eventual web frontend and
  management application share one role-scoped API with no direct database
  access; this API is that contract's first implementation.
- A daemon (`cmd/host`) and Docker packaging, mirroring `cmd/bridge`'s
  shape.

None of it claims Phase 1's go/stop gate is closed, and none of it is
cross-Bridge federation: `bridge/transport/`, `relay/`, and inventory
ingestion are untouched. A real `cmd/bridge` calling this Host's enrollment
API over the network is also **not** part of this slice — that needs
Bridge-side changes (an identity keypair, an HTTP client) that belong to a
follow-on slice, not this one.

### Resolved sub-decisions

Six design questions came up building this that were worth deciding
explicitly rather than silently:

1. **`auth.Store`'s concurrency contract, and why the obvious
   implementation is wrong.** `auth.Store`'s doc comment says "Load and
   Save must be callable inside one transaction, because a rotation that
   is not atomic reintroduces exactly the race this protocol exists to
   close." Reading `auth/refresh.go` line by line: `Store.Load` is **not**
   always followed by `Store.Save`. `Verifier.Rotate` has four early-return
   paths (unknown token, revoked family, wrong Bridge, a token matching
   neither hash) that call `Load` and return without ever calling `Save`;
   `Revoke` and `ReEnroll` each have one more (unknown token). The unknown-
   token case is explicitly documented as common, not exceptional — "an old
   backup being restored looks like this."

   That rules out the naive fix of holding a Postgres transaction open from
   `Load` to `Save`: it would leak an open transaction on ordinary,
   expected control flow, not only on panics. The atomicity boundary does
   not belong inside `Store` at all. `hoststore.BridgeCredentialStore`'s
   `Load`/`Save` are independent, single-statement, autocommit-equivalent
   operations (a `SELECT` and an `INSERT ... ON CONFLICT DO UPDATE`) — no
   transaction ever crosses the Go-level call boundary, so none of those
   Load-without-Save paths can leak anything.

   The actual hazard — two *concurrent* calls to any `Verifier` method for
   the *same* `protocol.BridgeID` — is closed instead by a per-`BridgeID`
   in-process mutex in `host/directory`, held for the whole duration of one
   `Verifier` method call (`defer unlock()`, so a panic or timeout still
   releases it). This is correct for a single Host process, which is all
   that's required today. If Host ever needs to run as more than one
   process, the upgrade path — recorded here as an open question, not
   built now — is a Postgres session-held advisory lock
   (`pg_advisory_lock`) acquired at the same call-wrapping layer: same
   shape, no change to `Store` or to `auth` itself.

   A related but separate race — two concurrent redemptions of the same
   single-use invitation by two *different* Bridge keys — is not covered
   by a per-`BridgeID` mutex at all (different keys mean different
   `BridgeID`s). It's closed by a single atomic conditional `UPDATE`
   (`WHERE ... AND use_count < max_uses`) on the invitation row: ordinary
   Postgres row locking inside one statement, no extra code needed.

   Also worth recording here so the HTTP error-mapping layer doesn't invent
   a dead branch: `auth.ErrGraceExpired` is declared but never returned by
   any code path — "previous token presented after the window closed" is
   deliberately treated identically to "presented twice," and both route
   through `Verifier`'s internal `revoke` helper, which always returns
   `ErrReuseDetected`.

2. **Package layout and driver.** `host/hoststore` (Postgres connection,
   schema, every concrete repository, the `auth.Store` implementation),
   `host/directory` (domain logic), `host/hostapi` (JSON HTTP,
   `net/http.ServeMux` with Go 1.22+ method-pattern routing — the same
   approach `bridge/adminui/server.go` already uses twice successfully in
   this repo, JSON instead of `html/template`), `cmd/host` (daemon
   entrypoint). Driver: `database/sql` plus `github.com/jackc/pgx/v5/stdlib`,
   not pgx's native interface — nothing in this slice needs COPY, LISTEN,
   or pipelining, and `database/sql` keeps every `*sql.DB`-touching package
   speaking the same idiom the rest of the Go toolchain understands.

3. **Bridges are global identities; Swarm membership is a join table.**
   `auth.FamilyState`/`Verifier` are keyed only by `protocol.BridgeID` — no
   `SwarmID` anywhere in `auth` — and `protocol/alias.go`'s own design
   (a Bridge can belong to multiple Swarms, each with an uncorrelatable
   alias) presumes one Bridge, many Swarms. So `bridges` holds one row per
   global identity; `bridge_swarm_memberships` is a join table; and
   `bridge_credential_families` is keyed by `BridgeID` alone, globally,
   because the credential protocol itself is global. The consequence is
   worth stating plainly: `RevokeBridge` kills a Bridge's Host access
   across every Swarm it belongs to. There is no "revoke from this Swarm
   only" in the `auth` protocol as it exists today — that would be a
   separate, much simpler operation (disable one `bridge_swarm_memberships`
   row, no `auth.Verifier` call at all), named below as a deliberate
   follow-on rather than built now.

4. **Login keys on a username, not email.** `docs/phase0/privacy-data-map.md`
   still has an open question — whether email is required for invitations
   at all, or an invitation code is sufficient on its own. Keying login on
   a required `username` field sidesteps that question entirely: nothing
   in this slice's correctness depends on its eventual answer, and `email`
   stays present in the schema but nullable.

5. **Argon2id for the password verifier, not Bridge admin UI's PBKDF2.**
   `privacy-data-map.md` specifies Argon2id for the Host's password
   verifier specifically. Bridge's admin UI (ADR 0015) deliberately used
   hand-rolled PBKDF2-HMAC-SHA256 to preserve Bridge's zero-Go-dependency
   streak. Host was never held to that bar — `go.mod`'s own comment
   anticipated dependencies arriving "with Phase 1 persistence and Phase 2
   Host work," and this slice is where that happens, via
   `golang.org/x/crypto/argon2`. The two packages making different choices
   is deliberate, not inconsistent: one is a data-map requirement for a
   central, higher-value credential store; the other was an explicit
   dependency-avoidance trade for a local, single-operator tool.

6. **No per-Swarm authorization model yet.** Every admin action in this
   slice (create a Swarm, issue an invitation, revoke or re-enroll a
   Bridge) requires only "is this session an owner account" — there is no
   per-Swarm role check, and no member self-service account creation
   beyond the single bootstrap owner. Recorded as an open question below,
   not solved here.

## Options considered

### Build the identity/enrollment slice now, JSON API only — chosen

Ships something real and independently useful (an operator can stand up a
Host, create a Swarm, and enroll a Bridge identity against it) without
touching the still-open go/stop gate or any of ADR 0006's open retention
questions. Cost: Phase 2 code exists before Phase 2 is formally reachable
in full, which this record makes visible rather than implicit — same
trade ADR 0015 made for `bridge/`.

### Wait for ADR 0006 (central plaintext and retention) to reach Accepted first

Rejected for this slice specifically: ADR 0006's open questions are exact
retention *window lengths* and whether moderators need titles past the
window — both about inventory, activity, and security-event data (data-map
§2–§4). This slice touches only §1 (identity/membership), whose fields
(user id, display name, password verifier, Swarm id/membership/role,
Bridge id, Bridge public key, credential hashes, alias keys) are already
fully specified. Waiting would block real, well-specified work on a
decision that doesn't govern it.

### Build inventory/grants/moderation alongside identity, as one large Phase 2 slice

Rejected: far larger surface, several genuinely open questions (ADR 0006's
retention windows, ADR 0012's relay grant-verification scheme), and no
need to resolve any of it before identity/enrollment — which everything
else in Host is built on top of — can exist and be proven on its own.

## Consequences

- `host/hoststore/`, `host/directory/`, `host/hostapi/`, and `cmd/host/`
  are new. `protocol/ids.go` gains `InvitationID`; `protocol/events.go`
  gains four event kinds (`user.registered`, `swarm.created`,
  `invitation.issued`, `bridge.reenrolled`), all `RetentionAudit` and
  title-free.
- `go.mod` gains its first two non-stdlib dependencies:
  `github.com/jackc/pgx/v5` and `golang.org/x/crypto`. Both are confirmed,
  at the point each is introduced, not to leak into `cmd/bridge`'s
  dependency graph (`go list -deps ./cmd/bridge` stays free of both) —
  Bridge's zero-dependency property from ADR 0015 is unaffected.
- A `Dockerfile.host` and an extended `docker-compose.yml` (a `postgres`
  service, a `host` service) exist at the repo root; the existing
  `Dockerfile`/`bridge` service are untouched.
- The Phase 0 go/stop table in `docs/phase0/acceptance-evidence.md` is
  unaffected — it still correctly reports the CGNAT hardware proof as the
  sole remaining gate. This ADR's existence, like ADR 0015's, is what keeps
  that true rather than quietly stale.
- `host/README.md`'s "Blocked on" list is corrected: ADR 0003 is Accepted
  and was never actually blocking anything Host needs; ADR 0006 is the
  real blocker, and only for the parts of Host this slice deliberately
  does not build (see below).

## Explicitly out of scope

Named here so nothing built later mistakes this slice for more than it is,
the same way ADR 0015 named what it deliberately deferred:

- The central inventory index, manifest/delta ingestion, and reconciliation
  against `preservation`/`reference` — all of Scope of Work §5.1's "central
  inventory index and change feed," "reference catalogue versioning,"
  "search and completion calculations," "preservation requests."
- Transfer grants and the relay's public-key grant-verification scheme
  (ADR 0012's open item — "the relay cannot call back into the Host on the
  hot path").
- A full event-store retention sweeper. This slice's `events` table has no
  sweeper at all — ADR 0006's open window-length questions genuinely block
  that, unlike the identity fields this slice touches.
- Moderation cases, announcements, leaderboards, aggregate metrics and
  receipts.
- Any Host web UI — this slice is JSON API only, the same relationship
  `bridge/adminui` had to `cmd/bridge`'s daemon-core-first sequencing.
- Wiring a real `cmd/bridge` to call this Host's enrollment API over the
  network — needs Bridge-side identity-key generation and an HTTP client,
  which belong to a follow-on slice.
- Per-Swarm authorization/RBAC, and per-Swarm partial Bridge removal
  (disabling one `bridge_swarm_memberships` row without a full global
  credential revocation).

## Open questions

- Per-Swarm authorization/RBAC for admin actions beyond "is this an owner
  account."
- Per-Swarm partial Bridge removal, as a separate, simpler operation from
  global `RevokeBridge`.
- Whether Host session tokens should grow a cookie-based mode once ADR
  0013 picks a frontend framework — this slice returns a bearer token in
  the JSON body only.
- Multi-process Host horizontal scaling, and the advisory-lock upgrade path
  named in sub-decision 1, if and when it's ever needed.
- Everything named "explicitly out of scope" above remains exactly as open
  as ADR 0006 and ADR 0012 already record; this ADR does not narrow either.
