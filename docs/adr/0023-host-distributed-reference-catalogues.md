# ADR 0023: Host-distributed reference catalogues

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-12
- **Scope of Work reference:** direct follow-on to ADR 0019 (inventory
  publishing, which first introduced the `SWARM_REFERENCE_DAT_<PLATFORM>`
  local bootstrap this record retires) and ADR 0004/0005 (initial
  platform set, default collection profile — the reference-catalogue
  concept those records already assume).

## Context

Every publish attempt needs a reference catalogue (a Logiqx/No-Intro-style
DAT) loaded per platform before anything can classify as
`ClassVerifiedEligible` — without one, `scanHoldings` reports every
holding on that platform as skipped, and nothing gets published. ADR
0019's original answer was `SWARM_REFERENCE_DAT_<PLATFORM>` env vars: each
Bridge operator sources their own DAT and points a Docker env var at a
locally mounted file.

Real setup (this session's live testing) surfaced two problems with that:

1. **It's real friction for an end user.** Every Bridge operator has to
   independently discover, download, and mount a DAT file per platform
   before their Bridge can publish anything — for a private Swarm where
   several Bridges are meant to share one library, that's the same setup
   step repeated once per Bridge for no reason.
2. **It's a real trust hole.** The user identified this directly: if a
   Bridge can supply its own catalogue, its operator can substitute a
   doctored one that classifies hacks, bad dumps, or arbitrary content as
   `verified_eligible`. Nothing downstream — the Host, or any other
   Swarm member reading the Host's inventory stats — has any way to tell
   that Bridge's "verified" claims were computed against a different,
   self-chosen ground truth. That defeats the entire reason a reference
   catalogue exists: for "verified" to mean the same thing regardless of
   which Bridge is asserting it.

The user first asked whether reference catalogues could simply be bundled
into the Docker image for zero-setup convenience. That was investigated
and explicitly rejected: even `libretro/libretro-database` — a
long-running, well-resourced open source project that mirrors No-Intro
DATs for RetroArch — does not assert a clear license to redistribute the
upstream catalogue data itself; its own contributor docs say to go
through the upstream group's own channels rather than claiming
redistribution rights. Bundling third-party verification data into this
project's distributed Docker image on that basis would be a real,
unresolved IP risk, not a convenience worth the exposure. See the
session's own investigation for the sources checked.

## Decision

**The Swarm owner uploads a reference catalogue once, through the Host's
web UI. It is distributed to every joined Bridge automatically, and it is
the sole authority for that platform — a Bridge cannot supply or fall
back to its own local catalogue once joined.**

1. **Host-authoritative, not Bridge-overridable, full stop.** The user's
   own reasoning (see Context) is the entire justification: verification
   has to mean the same thing across every Bridge in a Swarm, which
   requires exactly one ground truth per platform, controlled by the
   party the Swarm's other members are already trusting — the owner who
   set the Swarm up. An earlier draft of this decision considered letting
   a Bridge's own locally-loaded catalogue act as a fallback for a
   platform the Host hadn't covered yet; the user rejected that too, in
   favor of retiring the local bootstrap entirely. Once a Bridge is
   joined, `SWARM_REFERENCE_DAT_<PLATFORM>` no longer exists as a
   concept at all — there is nothing left to override.
2. **Swarm-scoped storage, one row per (Swarm, platform), no history.**
   Mirrors how invitations, Bridge memberships, and inventory snapshots
   are already scoped in this schema — an owner running multiple Swarms
   may want different platform coverage per Swarm. Re-uploading for a
   platform replaces whatever was there before outright; there is no
   version log; a stale prior catalogue serves no purpose once superseded.
3. **Refreshed before every publish, not fetched once at join.** The
   user confirmed this via `AskUserQuestion`. A Bridge calls
   `GET`-shaped `POST /api/bridges/{id}/reference-catalogues` at the
   start of every `PublishInventory` attempt (manual or automatic,
   ADR 0020's own cadence) and replaces `d.referenceSelections` wholesale
   with whatever the Host currently serves. An owner updating or adding a
   catalogue later reaches every already-joined Bridge on its own, with
   no re-join or manual Bridge-side action required — the same
   self-correcting principle ADR 0020 already established for inventory
   publishing itself.
4. **Validated on upload, not on every fetch.** `UploadReferenceCatalogue`
   runs the same `reference.ImportDAT` parser a Bridge would use, before
   ever storing anything — a malformed upload is rejected immediately
   with a clear error at the point the owner can act on it, rather than
   silently failing on every Bridge's next fetch. Bridges still parse the
   content again themselves on receipt (need their own
   `*reference.Selection`, not just proof it once parsed), but that
   second parse should be unreachable in practice.
5. **Content stored in Postgres, not the filesystem.** A DAT is at most a
   few megabytes (`reference.ImportDAT`'s own doc comment); storing raw
   bytes in `swarm_reference_catalogues.dat_content` keeps this
   consistent with every other piece of Host state (no new filesystem
   persistence concern for `cmd/host`'s deployment, which today has none).
6. **A transient fetch failure keeps the last-known-good set, not a wipe.**
   If the Host is briefly unreachable, `refreshReferenceCatalogues` logs
   and leaves `d.referenceSelections` exactly as it was — a network blip
   must not silently stop everything from verifying for one publish
   cycle. It self-corrects on the next attempt.

## Concrete design

### Host side

`host/hoststore/schema.sql`: new `swarm_reference_catalogues` table —
`(swarm_id, platform)` primary key, `dat_content BYTEA`,
`content_sha256`, `entry_count`, `filename`, `uploaded_by`, `uploaded_at`.

`host/directory/referencecatalogues.go` (new):
- `UploadReferenceCatalogue(ctx, account, swarm, platform, filename, content) error`
  — owner-scoped (reuses `GetSwarm`'s membership check), validates via
  `reference.ImportDAT`, upserts.
- `ListReferenceCatalogues(ctx, account, swarm) ([]ReferenceCatalogue, error)`
  — Host UI's own read, metadata only (no `Content`).
- `DeleteReferenceCatalogue(ctx, account, swarm, platform) error`.
- `GetReferenceCatalogues(ctx, bridge, presented, swarm) ([]ReferenceCatalogue, error)`
  — Bridge-facing read, `Content` included, gated by
  `auth.Verifier.Authenticate` plus a new shared
  `requireActiveMembership` helper (extracted from the previously
  inline-duplicated check in `SetBridgePublishedDisplayName`, ADR 0022).

`host/hostapi/referencecatalogues.go` (new): `POST
/api/bridges/{bridgeID}/reference-catalogues`, unauthenticated at the
route level like enroll/rotate/inventory/display-name — the refresh token
in the body is the credential. `{refresh_token, swarm_id}` in,
`{catalogues: [{platform, filename, content_base64, content_sha256,
entry_count}]}` out — always the complete current set, never a delta.

`host/hostui`: new "Reference catalogues" card on the existing Swarm page
— a table of what's loaded per platform with a Delete action, and a
`multipart/form-data` upload form (platform selector + file input).
`POST /swarms/{id}/reference-catalogues` and `POST
/swarms/{id}/reference-catalogues/{platform}/delete`, both going through
the same `Directory` methods above — no new API layer, matching how every
other owner-facing Swarm action in `host/hostui` already works.

### Bridge side

`bridge/hostclient.Client.FetchReferenceCatalogues(ctx, bridge, refresh,
swarm) ([]FetchedCatalogue, error)` — the outbound call, decoding
`content_base64` back to raw bytes.

`cmd/bridge/reference.go` (rewritten, not extended):
`Daemon.refreshReferenceCatalogues(ctx, hostURL, bridge, refresh, swarm)`
— fetches, parses each platform via `reference.ImportDAT` +
`reference.DefaultProfile().Apply`, and replaces
`d.referenceSelections` wholesale. Called from `PublishInventory`
(`cmd/bridge/inventory.go`) immediately before `scanHoldings`, inside the
same `publishMu`-serialized section that already guards
`d.referenceSelections`'s only other reader/writer — no new lock needed.

**Removed entirely**: `BootstrapReferenceCatalogues`,
`SWARM_REFERENCE_DAT_<PLATFORM>`, the `main.go` startup call, and the
corresponding `docker-compose.yml` env vars and `./reference-dats` bind
mount. There is nothing to configure on the Bridge for this anymore.

## Consequences

- A Bridge operator does zero reference-catalogue setup — join the
  Swarm, and whatever the owner has uploaded just works, updating
  automatically as the owner maintains it.
- The Swarm owner takes on real responsibility they didn't have before:
  sourcing and uploading catalogues is now solely their job, for every
  Bridge in the Swarm at once, not something each operator handles
  independently. This is the deliberate trade the user asked for.
- `reference` (previously Bridge-only) is now also imported by
  `host/directory`, for upload-time validation. This doesn't affect
  Bridge's own dependency-light build — `go list -deps ./cmd/bridge`
  still carries no Postgres/Host dependency, unaffected by the Host
  importing more packages of its own.
- ADR 0019's original `SWARM_REFERENCE_DAT_<PLATFORM>` decision is
  superseded by this record; that document is left as historical record
  of what was originally built, not rewritten.

## Explicitly out of scope

- Bundling any real reference catalogue data into the Docker image or
  git repository — investigated and rejected on licensing grounds; see
  Context.
- A version history of uploaded catalogues, or the ability to roll back
  to a previous upload. Re-uploading replaces outright.
- Any notion of per-Bridge catalogue scoping within one Swarm — every
  Bridge in a Swarm sees the same catalogues, matching how every other
  Swarm-scoped resource in this project already works.
- Automatic catalogue updates from an upstream source (No-Intro, Redump)
  — Scope of Work Phase 4 already names "whether the Host may retrieve
  approved DAT updates automatically" as deferred; this record doesn't
  reopen it.

## Open questions

- Whether a Bridge should have any way to tell it's using a stale
  catalogue if a fetch has been failing silently for a while (today: a
  log line only, easy to miss). Worth revisiting once the persistent
  publish-status work (ADR 0021) has a natural place to surface it.
- Whether large multi-Swarm Hosts will want catalogue re-use across
  Swarms (upload once, attach to several) rather than a fully separate
  upload per Swarm — no such cross-Swarm sharing exists anywhere in this
  schema today, so this stays Swarm-scoped until a real need appears.
