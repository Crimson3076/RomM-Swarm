# ADR 0019: Bridge inventory publishing to the Host

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-12
- **Scope of Work reference:** Phase 2/4 follow-on. ADR 0016's "explicitly
  out of scope" named "the central inventory index, manifest/delta
  ingestion, and reconciliation against `preservation`/`reference`" — all
  of Scope of Work §5.1's "central inventory index and change feed."
  ADR 0017 said plainly: "inventory publication is out of scope (ADR
  0016)." This record is that follow-on's first slice.

## Context

ADR 0016 through 0018 built a working, fully tested identity and trust
layer — accounts, Swarms, invitations, Bridge enrollment/rotation/
revocation, both a JSON API and two browser UIs. None of it lets a Bridge
tell the Host what ROMs it actually holds. The Host has nothing to cache,
and both UIs can only show Swarm/Bridge membership, not real numbers.

Reading the codebase directly (not assumed) turned up something important:
almost the entire domain model this feature needs already exists, built in
earlier phases and never wired to a Host. `protocol/manifest.go` already
defines the wire format (`Manifest`, `Item`, `Delta`). `protocol/ids.go`
already has content-derived `FileID`/`GameID`. `bridge/publish/publish.go`
already builds a policy-filtered `Manifest` from local scan results
(`Snapshot`, `Diff`, `Preview` — all tested). `protocol/events.go` already
reserves `EventInventorySnapshot`/`Delta`/`Tombstone`/`Reconcile`/`Drift`
and `EventCoverageSnapshot` in the closed event vocabulary, unused until
now. This record is mostly about building the transport and Host-side
storage this data was always meant to have — not inventing new domain
logic.

Two real gaps surfaced while designing this, both resolved below:

1. Enrollment (`Directory.RedeemInvitation`, ADR 0016) never hands a Bridge
   its own `SwarmID` or `BridgeAlias`. A Bridge cannot compute its alias
   itself — `protocol/alias.go`'s own doc comment says the per-Swarm alias
   key "never leaves the Host." Without the alias, `publish.Membership`
   (already built, already validated) can never pass its own `Validate()`.
   This is a real prerequisite this record has to close, not incidental
   plumbing.
2. Nothing in `cmd/bridge` ever calls `reference.ImportDAT` — confirmed by
   grepping the whole repository. A Bridge scanning its RomM library today
   would find zero verified, publishable items (correctly — no unverified
   content is ever advertised, per Scope of Work). Left alone, this feature
   would ship honestly inert.

This record also has to make a real design decision no existing code
precedents: how does a Bridge authenticate a call to the Host that isn't a
credential operation? Every Bridge-facing route ADR 0016 shipped IS a
credential op — `POST /api/bridges/enroll` and `.../rotate` both work
because "the refresh token presented in the request body IS the
credential" (`host/hostapi/server.go`'s own comment). Inventory publishing
is the first Bridge-authenticated, non-credential call this codebase has
ever needed.

## Decision

**Build the transport and Host-side storage for Bridge inventory
publishing, as a narrow "slice 1" — a full-snapshot publish of everything
already verified-publishable, stored and indexed just enough for basic
cross-Bridge stats, manually triggered, no scheduler.**

### Resolved sub-decisions

1. **A new, read-only `auth.Verifier.Authenticate(bridge, presented Token)
   error`** authenticates the publish call — not a reuse of `Rotate`, and
   not a new short-lived access-token mechanism. `Authenticate` performs
   exactly `Rotate`'s early validation (`Load`, revoked check, BridgeID
   match, `presented.Hash()` against `CurrentHash` only — deliberately
   *not* accepting `PreviousHash`/grace-window logic, since that exists to
   handle token *consumption*, which a read-only check never does). It
   never calls `Save`, records no event of its own, and mutates nothing.
   Reusing `Rotate` itself was rejected: every "Publish Inventory" click
   would silently rotate the Bridge's refresh credential as an unrelated
   side effect, and it creates a bad failure mode — if rotation succeeds
   but the manifest write then fails, the Bridge must still persist the
   newly-issued token immediately or fall out of sync, turning every
   publish attempt into an unrelated echo of the crash-recovery scenario
   the grace-window protocol exists to handle. A real short-lived access
   token was also rejected for this slice: `auth.Result.AccessExpiresAt` is
   already computed today, but no token value is ever minted or checked
   anywhere in this codebase — building that is genuine new protocol
   surface, better done deliberately once a second consumer exists to
   justify it, not smuggled in as a side effect of this record.
2. **`RedeemInvitation` hands back `SwarmID` and `BridgeAlias`.** The
   method already has `swarmID` in scope and discards it on return; it now
   also reads the Swarm's `alias_key` (already stored, already generated
   at Swarm creation — ADR 0016) and computes `protocol.AliasFor`, adding
   both to its return value and to `hostapi`'s enrollment response. Purely
   additive on the wire; no existing enrollment behavior changes.
3. **Full `Manifest` snapshot only, every publish — no `Delta` in this
   slice.** `publish.Diff`/`protocol.Delta` are real, tested, and untouched
   by this record, but using them here would require either new
   Bridge-side persisted state (the previous full manifest, kept just to
   diff against) or an extra Host round trip to fetch it back — real
   complexity unjustified for a private, invitation-only federation with
   realistically small per-item payloads. Replacing the whole snapshot on
   every publish also sidesteps an entire class of correctness questions
   (out-of-order revisions, partial-delta application, drift detection)
   that belong to a later, real central-inventory-index record, exactly as
   ADR 0016 already deferred them.
4. **Publish policy is hard-coded to `ShareAll: true` for this slice.**
   Nothing anywhere persists an owner's chosen sharing policy today;
   building that configuration surface (per-platform/per-file exclusions,
   a settings UI) is separate scope. This slice still exercises
   `publish.Snapshot`'s real filtering logic — `Classification.
   Publishable()` is the one rule that actually matters for correctness,
   and it's still enforced.
5. **A minimal, env-var-only `SWARM_REFERENCE_DAT_<PLATFORMID>` bootstrap
   is included in this slice.** Mirrors `SWARM_HOST_URL`/
   `SWARM_INVITATION_CODE`'s existing convenience idiom exactly: read once
   at startup, non-fatal on a bad or missing path, no UI. Loads a DAT via
   `reference.ImportDAT` + `reference.DefaultProfile().Apply` into an
   in-memory `map[protocol.PlatformID]*reference.Selection` on `Daemon`.
   Without this, the feature would ship correct but permanently inert — no
   Bridge would ever have anything verified to publish, and the manual
   verification step (below) could never demonstrate real numbers. Full DAT
   catalogue lifecycle management (upload, versioning, a real admin-UI
   page) stays separate, future scope — this is deliberately the smallest
   possible thing that makes the rest of this record demonstrable.
6. **No `bridgeLocks` mutex for inventory publishing.** That per-BridgeID
   mutex (`host/directory/bridgelocks.go`) exists specifically to close
   `auth.Store`'s Load-then-later-Save race on `bridge_credential_families`
   (ADR 0016, resolved sub-decision 1). `Authenticate` never calls `Save`,
   and the inventory write path (`InventoryStore.Replace`) gets its own
   atomicity from a single Postgres transaction — there is no cross-call
   read-modify-write hazard here for a mutex to close.
7. **Manual-trigger only — no background scheduler.** A "Publish
   Inventory" button, mirroring "Test Swarm Connection" exactly, not a
   ticker. Consistent with this project's existing choice not to build
   automatic credential rotation yet; ADR 0017's own open questions already
   named inventory publication as the eventual trigger for revisiting that
   decision. This record doesn't reopen it.

## Options considered

### Build the transport + storage now, reusing existing domain logic as-is — chosen

Nearly the entire hard part (wire format, canonical identifiers, policy
filtering, coverage-stat math) already exists and is already tested. The
remaining work is transport, persistence, and the two real gaps named
above — closing those unlocks the domain logic that has sat unused since
earlier phases.

### Build the real central inventory index (full history, `Delta` replay, `preservation.ComputeCoverage` risk buckets) in one record

Rejected as far too large a single slice, and inconsistent with this
project's own incremental-ADR discipline (ADR 0016 was explicitly "slice
1: identity," leaving the rest of Scope of Work §5.1 for later). Full
coverage-risk assessment needs a reference-set "expected" list the Host
doesn't have any way to obtain yet; that's real, separate scope.

### Reuse `auth.Verifier.Rotate` as the publish call's authentication step, instead of adding `Authenticate`

Rejected — see resolved sub-decision 1. The coupling between "prove who you
are" and "also advance your credential" is exactly the kind of surprising
side effect this project's existing rotation protocol was built to avoid
in every other context.

### Leave reference-catalogue loading out of this record entirely, ship the feature honestly inert

Considered and explicitly decided against, after discussion: without any
way to load a DAT, every real-world publish would return "nothing to
publish," and neither UI could ever be manually verified against real
numbers. The minimal env-var bootstrap (sub-decision 5) is the smallest
addition that avoids that outcome without taking on real DAT lifecycle
management as part of this record.

## Consequences

- `auth/refresh.go` gains a new, non-mutating verification method —
  `host/hoststore`'s first repository type to call something other than
  `Load`/`Save` for a credential-adjacent check.
- `host/hoststore` gains its first multi-statement transaction
  (`InventoryStore.Replace`). This does not violate the package's existing
  "no transaction survives a Go-level call boundary" doc comment — that
  comment is about a transaction leaking *across* separate calls (the
  `auth.Store` Load-then-Save hazard ADR 0016 diagnosed and fixed with
  `bridgeLocks`); `Replace` opens and closes its transaction entirely
  within one call, which is the property that doc comment actually cares
  about.
- `host/hostapi`'s pre-existing lack of any request body size limit is
  fixed everywhere, not just for the new route — every existing handler
  gets an explicit, generous default cap via `http.MaxBytesReader`.
- `cmd/bridge`'s `Daemon` calls `bridge/publish` for the first time
  anywhere in this codebase, and — via the new DAT bootstrap — calls
  `reference.ImportDAT` for the first time outside `cmd/swarm-verify` and
  test code.
- `go list -deps ./cmd/bridge` must stay exactly as free of `pgx`/
  `x/crypto`/any `host/...` package as it already is — this record adds an
  outbound HTTP call and local scan/reference logic, not a dependency on
  Host's own code.
- Both `bridge/adminui`'s `/swarm` page and `host/hostui`'s Swarm view page
  gain new content; neither gains a new page — nesting under the existing
  Swarm page, the same navigational choice ADR 0018 already made for
  invitations and Bridges.

## Explicitly out of scope

- Full `preservation.ComputeCoverage` risk assessment (Resilient/AtRisk/
  Fragile/Stale/Unavailable buckets) — needs a reference-set "expected"
  list the Host has no way to obtain yet.
- `Delta`-based incremental sync — full snapshot only in this slice; see
  resolved sub-decision 3.
- Any owner-configurable publish policy (per-platform/per-file exclusions,
  a settings UI) — hard-coded to share everything publishable.
- Real DAT catalogue lifecycle management: upload, versioning, per-platform
  association through a UI, automatic reference-catalogue updates (Scope of
  Work explicitly defers "whether the Host may retrieve approved DAT
  updates automatically" to Phase 4). The env-var bootstrap in this record
  is a deliberately minimal stand-in, not that feature.
- Automatic or scheduled publishing — manual trigger only in this record.
  Superseded by [ADR 0020](0020-automatic-inventory-publishing.md), which
  adds a background scheduler and event triggers on top of the manual
  path this record ships.
- Real short-lived access-token authentication — `Authenticate` presenting
  the refresh token itself is this slice's answer; a real access-token
  mechanism is deferred, named as an open question below.
- Cross-Swarm aggregation of any kind (ADR 0013 territory) and multi-Swarm
  Bridge support (`cmd/bridge`'s `Daemon` already supports exactly one
  Swarm per process, unrelated to this record; inherited, not introduced).
- Transfer/preservation requests and any other item Scope of Work §5.1
  names beyond "central inventory index and change feed" basics.

## Manual verification

Real three-process run: one `cmd/host` against a real, freshly created
Postgres database, two independent `cmd/bridge` daemons each against their
own small standalone fake RomM HTTP server (real, structurally valid
Game Boy cartridge images from `internal/romfixture`, not `internal/fakeromm`'s
fixed non-analyzable payload — this needed content a real scan could
actually canonicalize and classify), all driven over real HTTP with no
shortcuts through Go's test harness.

Setup: a Swarm with two invitations, one per Bridge. Each Bridge bootstrapped
entirely from env vars (`ROMM_URL`/`ROMM_TOKEN`, `SWARM_HOST_URL`/
`SWARM_INVITATION_CODE`, `SWARM_REFERENCE_DAT_GB` pointing at one shared
fixture DAT — realistic, since two Bridges pulling from the same public
No-Intro catalogue is the common case) and then had its admin password set
via `/setup`, exactly as an operator would. Bridge A's fake RomM held
`Shared Game (USA)` and `Bridge A Only (USA)`; Bridge B's held the same
`Shared Game (USA)` payload (byte-identical, to prove cross-Bridge
deduplication rather than assume it) plus `Bridge B Only (USA)`.

Publishing each Bridge's inventory through the real
`POST /api/swarm/publish-inventory` route (the same one the admin UI's
button calls) produced:

- Bridge A alone: `{"published":true,"item_count":2,"distinct_files":2,...}`
- Bridge B, after A: `{"published":true,"item_count":2,"distinct_files":3,...}`

`distinct_files` climbing from 2 to 3 (not 4) on Bridge B's publish is the
dedupe proof: the shared file counted once, swarm-wide, not twice. The real
Host UI's Swarm page (`GET /swarms/{id}`, the same route a browser renders)
confirmed the same arithmetic independently: "3 distinct file(s) across 4
published holding(s), 1 held by more than one Bridge," with both Bridges'
rows showing 2 items and a real `LastPublishedAt` timestamp instead of
"never." This is the one property no single-Bridge unit test can
demonstrate — two independently-scanning Bridges converging on one
correct swarm-wide count required a second real Bridge process to prove.

## Open questions

- Whether the alias-mismatch check in `Directory.PublishInventory` should
  hard-reject or merely log-and-accept a mismatch. A mismatch today can
  only mean a bug or bit-rotted local Bridge state — the alias key never
  left the Host, so it cannot be a forged value from an attacker who
  doesn't already hold the refresh token. Hard-reject is the conservative
  default chosen here; worth a second look once real operators exist.
- Whether `inventory_items.canonical_size` (and the resulting
  `total_bytes` on the snapshot) edges toward "coverage" scope, which this
  record otherwise deliberately excludes. Included here as cheap now,
  expensive to backfill later against real data — flagged rather than
  silently assumed correct.
- When real short-lived access-token authentication should replace
  "present the refresh token itself" (resolved sub-decision 1) — named the
  same way ADR 0017 named its own deferred rotation-scheduler question,
  since this record's existence is plausibly what eventually triggers that
  follow-on.
- Whether multi-Swarm Bridge support is ever built, and if so how
  `PublishInventory`'s Daemon-side shape (currently assuming exactly one
  joined Swarm) should change — inherited from `JoinSwarm`'s existing
  single-Swarm limitation, not a new constraint this record introduces, but
  worth a future record's attention rather than silent assumption.
