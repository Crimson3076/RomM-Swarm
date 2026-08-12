# ADR 0021: Persistent, pollable inventory-publish status

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-12
- **Scope of Work reference:** direct follow-on to ADR 0019/0020 (Bridge
  inventory publishing, manual and automatic).

## Context

With ADR 0020 shipped, publishing runs automatically — which made a real
gap obvious: there was no way for an operator to tell a publish that was
still working (a large library takes minutes to scan and hash) from one
that had hung or silently failed. `handlePublishInventory` blocked for up
to `publishInventoryTimeout` (10 minutes) before answering at all, and the
only feedback was whatever that one blocking response happened to say —
nothing survived a page reload, and nothing was visible for automatic
publishes at all, which never touch the admin UI's HTTP layer in the first
place.

The user asked for this directly: a persistent, reload-safe indicator of
progress, not just a result.

## Decision

**`Daemon` tracks one `adminui.PublishStatus` in memory, updated throughout
every `PublishInventory` call (manual or automatic), readable without
blocking behind an in-flight publish; the manual "Publish Inventory" button
becomes fire-and-poll instead of blocking.**

1. **`PublishStatus` is a state machine, not just a result.** Phases:
   `idle` → `scanning` → `publishing` → `done` | `error`. `scanning`
   additionally carries `Platform`/`PlatformIndex`/`PlatformTotal` (which
   of the Bridge's fixed, known platform list is being scanned, out of how
   many) and a running `ItemsScanned` count. `Result`/`Err` hold the most
   recently *finished* attempt's outcome and are deliberately left
   untouched while a new attempt starts, so the UI shows the last known
   outcome instead of flashing back to empty the moment a scan begins.
2. **Per-platform progress, not per-item.** `protocol.InitialPlatforms()`
   is a small, fixed, known-length list — "platform 2 of 5" is cheap,
   real, and already meaningful context for whether a scan is moving.
   Per-item progress inside a single platform's listing would need
   threading a progress callback through `bridge/scan.Scanner`'s tested,
   stable public API for a smaller marginal benefit; left as a possible
   future refinement, not built here.
3. **A separate `statusMu`, not `publishMu`.** A status poll must never
   block behind a multi-minute publish in progress — only behind the brief
   moment another poll or update is touching the struct itself. `publishMu`
   (ADR 0020) still serializes the actual work; `statusMu` only guards the
   status snapshot.
4. **`TriggerPublishInventory`, not an inline `if !Running() { go ... }` in
   the HTTP handler.** The naive version has two races: (a) the response to
   the very call that started a publish could still say `running:false`,
   since nothing guarantees the spawned goroutine reaches
   `PublishInventory`'s own status update before the handler's own
   immediate status read — an operator could click the button and see
   "idle" for a moment, exactly the ambiguity this record exists to close;
   (b) two nearly-simultaneous calls (an impatient second click) could each
   independently decide nothing is running and both spawn a redundant full
   rescan back to back. `TriggerPublishInventory` closes both: an
   `atomic.Bool` `CompareAndSwap` decides, atomically, whether this caller
   is the one that gets to start a new attempt, and — critically — the
   status is marked `scanning` *synchronously*, before the function
   returns, not from inside the later-scheduled goroutine.
5. **The manual trigger and the status poll are two different routes.**
   `POST /api/swarm/publish-inventory` (`TriggerPublishInventory`) and
   `GET /api/swarm/publish-status` (`PublishStatus`) return the same JSON
   shape (a shared `writePublishStatusJSON` helper), so the page's polling
   JS doesn't need two response formats — the POST's response is just the
   first sample, from the same subsequent GET a reload would also fetch.
6. **Rendered server-side, not only client-side.** `swarmPageData` carries
   the live status fields on every render of `/swarm`, so a page reload
   mid-publish shows the real current state immediately — this is the
   literal ask ("persistent, even when reloading the page"). `static/swarm.js`
   only exists to keep the page live *without* a reload while a publish is
   running; it renders from the same JSON shape the server-rendered HTML
   already reflects, so the two can never drift into disagreeing states.

## Consequences

- `handlePublishInventory`'s response contract changed: it used to return
  the finished result synchronously; it now returns the current
  `PublishStatus` immediately, which may still be `scanning`. This is
  purely an internal contract (this JSON is consumed only by this
  package's own JS), so nothing external depended on the old shape.
- Verified the `TriggerPublishInventory` race fix empirically, the same
  way prior concurrency fixes in this project are verified: the naive
  "check-then-spawn" version was implemented first, its own new test
  (`TestPublishStatusEndpointReportsRunningWhileInProgress`) failed
  (`running = false, want true`) on the very first run, confirming the
  race was real and not theoretical; the `CompareAndSwap` + synchronous
  pre-mark fix was then verified clean across 10 repeated runs under
  `-race`.
- Automatic publishes (ADR 0020's scheduled ticks, and `JoinSwarm`'s
  post-join trigger) still call `PublishInventory` directly, not through
  `TriggerPublishInventory` — they have no "impatient double click"
  concern (one ticker, one join event), so the extra guard would add
  nothing. A rare cross-source overlap (a scheduled tick firing at the
  exact moment a manual click also fires) is still made safe by
  `publishMu`, just not deduplicated — an accepted, minor inefficiency
  (one redundant rescan in an already-rare window), not a correctness gap.

## Explicitly out of scope

- Per-item scan progress within a single platform (see resolved
  sub-decision 2).
- Persisting `PublishStatus` to disk — it is process-memory only,
  reset on restart. `swarmconn.Config`'s own durable
  `LastPublishedRevision`/`LastPublishedAt` (ADR 0019) is unaffected and
  still survives a restart; this record only adds the live, in-process
  view on top of it.
- Surfacing publish status on the Host UI's Swarm page — this is
  Bridge-local operator feedback about the Bridge's own work, not
  something the Host has any part in.

## Open questions

- Whether per-item progress is worth adding later, once real usage shows
  whether "platform N of M" is granular enough for a very large single
  platform's scan to still feel responsive.
