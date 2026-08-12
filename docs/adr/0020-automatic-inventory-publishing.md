# ADR 0020: Automatic inventory publishing

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-12
- **Scope of Work reference:** §5.1 (central inventory index); direct
  follow-on to ADR 0019, whose "Explicitly out of scope" section named
  "Automatic or scheduled publishing — manual trigger only" as the one
  thing this record now supersedes.

## Context

ADR 0019 shipped a real, tested publish path — scan, classify, snapshot,
send — but only behind a manual "Publish Inventory" button. The user asked
for this directly: Bridges should keep the Host's view of their library
current on their own, on a schedule and around the events that actually
change what a Bridge holds or who it's connected to, not only when an
operator remembers to click a button.

The request named several triggers: a periodic timer, joining a Swarm,
container start, container stop, and file changes. Not all of these are
equally implementable. Container **stop** has nothing new to publish and
no safe way to delay shutdown for an HTTP round trip, so it is explicitly
not built here — see Explicitly out of scope. The user confirmed, via
`AskUserQuestion`, the concrete scope below: on by default once joined, a
15-minute default interval, and two event triggers (startup-if-joined,
immediately-after-joining) rather than also wiring a per-import trigger.

## Decision

**A single background loop per Daemon, started unconditionally at boot,
plus one additional trigger on `JoinSwarm` — both funnelled through the
same `PublishInventory` call the manual button already uses, now
serialized by a new `Daemon.publishMu`.**

1. **On by default, no settings-page toggle.** Joining a Swarm already
   means "share my verified library with it" (ADR 0019's `ShareAll: true`
   default policy) — auto-publish just keeps that promise current instead
   of requiring a second, separate opt-in for the same intent. Consistent
   with `ShareAll`'s own precedent: this slice adds no new policy surface.
2. **Default interval 15 minutes, `BRIDGE_PUBLISH_INTERVAL_MINUTES`
   overrides it.** Mirrors `HOST_MAX_INVENTORY_BYTES`'s non-fatal-fallback
   convention: an unset, non-numeric, or non-positive value logs a warning
   and falls back to the default rather than failing to start.
3. **The loop starts unconditionally at boot, not only when already
   joined.** A Bridge can join a Swarm later, through the admin UI, at any
   point in its running lifetime — gating the loop's existence on
   boot-time join state would mean a Bridge that joins mid-life never
   gets scheduled publishing without a restart. Each tick checks join
   state itself (via `PublishInventory`'s own `swarmconn.ErrNotJoined`)
   and silently no-ops when there's nothing to publish yet.
4. **Two triggers beyond the timer: startup-if-already-joined, and
   immediately after `JoinSwarm` succeeds.** The first is `StartAutoPublish`
   running one publish attempt before entering its ticker loop — covers a
   container restart, so the Host isn't left showing data from before the
   restart for a full interval. The second is `JoinSwarm` itself spawning
   a background, best-effort publish right after persisting the new Swarm
   connection — covers both an interactive join through `/swarm` and
   `BootstrapSwarm`'s own env-var-seeded join, since both call the same
   `JoinSwarm`. A newly-enrolled Bridge shows real stats on the Host
   immediately rather than waiting for the next tick.
5. **`Daemon.publishMu` serializes every `PublishInventory` call, manual or
   automatic.** Without it, two calls racing (a scheduled tick overlapping
   a manual click, or the post-join publish overlapping an unlucky first
   tick) can each `Load` the same pre-publish `swarmconn.Config`, both
   succeed against the Host, and then race `Save` — the second write wins
   and the first publish's revision bump is silently lost, even though the
   Host actually received and stored both manifests. The same class of
   hazard ADR 0016's `bridgeLocks` closes for credential rotation, on the
   Bridge side this time and needing only one mutex, since a Bridge
   publishes to exactly one Swarm connection at a time (ADR 0019's own
   open question about multi-Swarm Bridges is unaffected — this still
   assumes exactly one).

## Options considered

### Gate the auto-publish loop on boot-time join state — rejected

Simpler to reason about at a glance, but wrong for the common case: an
operator who joins a Swarm through the running admin UI, not at container
start, would get no scheduled publishing until the next restart. Checking
join state on every tick instead of once at startup costs one extra
`swarmStore.Load()` per attempt and removes that gap entirely.

### Wire a per-import trigger (publish after each completed transfer) — deferred

Named in the original request ("file updates") but not selected — the
user chose the startup and post-join triggers only, leaving the periodic
timer to catch new holdings within one interval instead. A per-import
trigger would also need debouncing (a bulk-import session completing many
transfers in quick succession must not fire one publish per file), which
is real added complexity better justified once the 15-minute default
proves too slow in practice for a real deployment.

### Publish once, synchronously, from within JoinSwarm — rejected

Would block the `/swarm` join form's HTTP response on a full scan-and-
publish round trip, which can legitimately take minutes for a large
library — the same reasoning `StartImport` already backgrounds a transfer
rather than blocking the request that started it. Backgrounded instead,
on its own context (not the request's, which the HTTP handler cancels as
soon as it returns).

## Consequences

- A Bridge's owner no longer has to remember to click "Publish Inventory"
  for the Host to reflect reality — the common case (nothing changed since
  the last tick) costs one cheap `ErrNothingToPublish`-free scan every 15
  minutes, not a network call, since `PublishInventory` only calls the
  Host once a non-empty manifest is actually built.
- Every scheduled or startup failure is logged to stderr, never returned
  or panicked — a hung Host or an expired RomM connection degrades to "the
  Host's data goes stale until the next successful tick," not a crashed
  daemon.
- `publishMu` is a real, if narrow, addition to `PublishInventory`'s
  contract: every future caller of it must accept that a call may block
  briefly behind a concurrent one, which is correct today (nothing calls
  it from a latency-sensitive path) and worth remembering if that changes.

## Update (2026-08-12): timeouts raised for a Bridge not on RomM's local network

The per-attempt timeout wrapping every `PublishInventory` call (`5 minutes`
originally, an internal constant not otherwise discussed above) proved too
tight for real usage: an operator whose Bridge and RomM server aren't on
the same local network hit `context deadline exceeded` partway through a
scan, on top of `bridge/romm.Client`'s own separate, hard-coded 30-second
per-HTTP-request timeout (tuned for a same-LAN connection) failing
individual list/download calls even sooner.

Both are now configurable and raised: `DefaultPublishTimeout` (30 minutes,
`BRIDGE_PUBLISH_TIMEOUT_MINUTES`, `cmd/bridge/autopublish.go`) bounds the
whole attempt across every platform; `DefaultRommHTTPTimeout` (2 minutes,
`BRIDGE_ROMM_HTTP_TIMEOUT_SECONDS`, `cmd/bridge/rommtimeout.go`) is applied
to the live `romm.Client`'s `HTTP.Timeout` right after `Reconnect`
succeeds, overriding `bridge/romm`'s own same-LAN-tuned default for every
subsequent list and download call a scan makes. Neither change touches
`bridge/romm` itself — that package's default stays correct for its own
tests and for the common same-LAN case; the override is entirely at the
`cmd/bridge` layer, the same pattern `BRIDGE_PUBLISH_INTERVAL_MINUTES`
already established.

## Explicitly out of scope

- **Container-stop triggered publishing.** There is nothing new to publish
  on shutdown that a call moments earlier (the last tick, or the one about
  to fire) wouldn't already have sent, and delaying `SIGTERM` handling for
  an HTTP round trip risks the orchestrator (Docker, systemd) escalating
  to `SIGKILL` mid-request. Not built.
- **A per-import (file-change) trigger.** Named in the original request,
  deliberately not selected this round — see Options considered.
- **A settings-page toggle to disable auto-publish.** On-by-default,
  matching `ShareAll`; an explicit opt-out is real future scope if an
  operator ever needs it, not built here.
- **Adaptive or backoff intervals.** One fixed interval, operator-set via
  env var; no logic that shortens or lengthens it based on Host load,
  failure history, or library size.

## Open questions

- Whether a per-import trigger (with debouncing) is worth adding later,
  once real usage shows whether the 15-minute default is too slow for an
  actively-curating library — named so a future record doesn't have to
  re-derive why it was skipped this round.
- Whether the interval should ever become Host-configurable (a Swarm-wide
  policy) rather than purely Bridge-local — no such policy surface exists
  anywhere in this project yet, so this stays Bridge-local for now.
