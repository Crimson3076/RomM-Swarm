# ADR 0018: Network Host web UI

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-12
- **Scope of Work reference:** §5.4, ADR 0016's own out-of-scope bullet
  naming this follow-on: "Any Host web UI — this slice is JSON API only,
  the same relationship `bridge/adminui` had to `cmd/bridge`'s
  daemon-core-first sequencing." ADR 0013 (still Open, Phase 5) does not
  gate this — see Context.

## Context

ADR 0016 shipped a fully working, fully tested Host JSON API. Operating it
requires `curl` and hand-typed bearer tokens — exactly what running this
session's own manual verification looked like, twice now. ADR 0017 then
proved a real Bridge can actually enroll against a real Host over real
HTTP, with a live, enrolled Bridge sitting in the database as evidence.
Neither can be seen or managed through a browser.

`bridge/adminui` (ADR 0015) already established the precedent this record
follows: "a local, single-instance admin UI is a different category from
the cross-Swarm member portal ADR 0013 governs." ADR 0013 itself is
explicit that it covers only §5.3/§5.4's two cross-Swarm-facing clients
(the future `web/` portal and the management application) — "No frontend
code should be written before this is decided, and none has been" — and
that sentence is only coherent if a *local* admin UI is understood as a
different category, since `bridge/adminui` already existed when ADR 0016
was written and referenced ADR 0013 without contradiction. The same
reasoning applies here without needing ADR 0013 to move.

## Decision

**A new `host/hostui` package, mirroring `bridge/adminui`'s file layout,
served by its own `*http.Server` on its own port — not routes added to
`hostapi.Server`.**

`hostapi`'s own package doc comment says "No HTML here"; its
`requireOwnerAuth` middleware's own comment says "there is no
redirect-based flow here... since this API has no browser pages of its
own." Both are correct as written and shouldn't be contorted to serve two
different failure-mode contracts (JSON 401 vs. HTML redirect) based on path
prefix. `hostui` and `hostapi` are two independent HTTP front ends over the
same `*directory.Directory` — the same relationship a hypothetical future
Bridge JSON API would have with the already-existing `bridge/adminui`, just
built in the other order here.

### Resolved sub-decisions

1. **`hostui.Server` holds a concrete `*directory.Directory`, not an
   interface** — deliberately unlike `bridge/adminui.Backend`.
   `hostapi.Server` already set this precedent (`Server{Directory
   *directory.Directory}`, tested throughout against real Postgres). This
   package's whole convention, since ADR 0016, has been "test against a
   real database, gated by `TEST_POSTGRES_DSN`," not "fake the
   dependency" — `hostui` follows suit rather than inventing a narrower
   interface Bridge's very different (database-free) architecture needed
   but Host's doesn't.
2. **No separate in-memory `sessionStore` type.** Read directly from
   source: `Directory.Authenticate`/`ValidateSession`/`Logout`
   (`host/directory/accounts.go`) take and return plain `SessionToken`
   values with zero reference to `http.Request`/`ResponseWriter` anywhere
   — fully transport-agnostic — and already durably persist sessions in
   Postgres (`sessions` table, `token_hash`/`account_id`/`expires_at`).
   `bridge/adminui`'s in-memory `sessionStore` exists because Bridge has no
   database at all to lean on; duplicating that pattern here, on top of a
   store that already durably tracks the same thing, would be pure
   redundancy. `hostui`'s cookie middleware calls `Directory.ValidateSession`
   directly on every request instead.
3. **Cookie expiry mirrors `Authenticate`'s real returned `expiresAt`
   exactly**, rather than inventing a second, independently-configured
   constant the way Bridge's in-memory `sessionLifetime` had to (Bridge's
   session and its cookie are the same made-up thing; Host's session is a
   real database row with a real expiry already computed once).
4. **Global `RevokeBridge`/`ReenrollBridge` stay global; the UI nests them
   under one Swarm's page for navigation only, not for scope.** ADR 0016's
   resolved sub-decision 3 already established that revocation kills a
   Bridge's Host access across every Swarm it belongs to — there is no
   per-Swarm variant in the `auth` protocol as it exists. The confirm copy
   on the revoke action says this plainly, so an owner clicking "revoke"
   from inside one Swarm's page isn't misled about the blast radius.
5. **Four new, narrow, additive `Directory` methods, built and tested
   before `hostui` itself exists**: `OwnerExists`, `GetSwarm`,
   `ListInvitations`, `ListBridgesForSwarm`. None become new `hostapi` JSON
   routes in this slice — `hostui` calls `Directory` in-process directly,
   the same relationship `hostapi.Server` already has with it. Exposing
   these as JSON routes for a future `web/` portal is a deliberate,
   deferred follow-on (ADR 0013-gated, not decided here).
6. **The one-time invitation code is rendered directly in the POST
   response, never redirected-to, never held server-side anywhere in
   `hostui`.** `IssueInvitation`'s own doc comment already guarantees the
   plaintext code is "never retrievable again" — the UI must not
   contradict that by caching it in a session, a query parameter, or
   anywhere else it could leak or be re-displayed. No existing precedent in
   `bridge/adminui/templates/` for a comparable one-time-secret display;
   designed fresh, with an explicit "will not be shown again" warning in
   the response page.

## Options considered

### A second server, same process, sharing one `*directory.Directory` — chosen

Both `hostapi` and `hostui` are inside the same trust boundary already
(same process, same database credential); running them as two listeners on
two ports keeps `hostapi`'s "JSON API only, no browser pages" property
literally true for anyone who only exposes that port, while letting
`hostui` reuse every piece of tested domain logic in `host/directory`
without modification.

### Routes bolted onto `hostapi.Server`

Rejected: contradicts that package's own stated design (see Decision
section above) and would require either duplicating `requireOwnerAuth`
into a second, cookie-aware middleware inside the same package, or
contorting one middleware into two different failure-mode contracts keyed
off path prefix.

### A separate binary/process talking to `hostapi` over HTTP, like an external client

Rejected as needless indirection: `hostui` and `hostapi` already share a
process and a database connection; routing UI actions through an extra
HTTP hop to reach the same `Directory` would add latency and a whole new
class of transport-error handling for no isolation benefit, since nothing
about `hostui`'s trust level differs from `hostapi`'s.

## Consequences

- `host/hostui/` is new (`server.go`, `auth.go`, `setup.go`, `login.go`,
  `dashboard.go`, `swarms.go`, `bridges.go`, `templates/*.html`,
  `static/style.css`).
- `host/directory/` gains four new methods across its existing
  `accounts.go`, `swarms.go`, `invitations.go`, `bridges.go` files — no new
  files there.
- `cmd/host/main.go` now runs two `*http.Server`s (the existing JSON API,
  plus this UI on a new `HOST_UI_LISTEN_ADDR`), started as separate
  goroutines and shut down together.
- `Dockerfile.host` and `docker-compose.yml`'s `host` service expose a
  second port.
- Zero new Go dependencies — `html/template`, `embed.FS`, and
  `net/http.ServeMux` are already proven twice in this repo
  (`bridge/adminui`, and now this).
- The Phase 0 go/stop table remains unaffected, the same way it was
  unaffected by ADR 0015, 0016, and 0017 — this record's existence is what
  keeps that true rather than quietly stale.

## Explicitly out of scope

- New `hostapi` JSON routes for the four new `Directory` reads — deferred
  until a future client (`web/`, ADR 0013-gated) actually needs them over
  the wire rather than in-process.
- Per-Swarm authorization/RBAC for the UI — every owner account has global
  authority, same as `hostapi` today (ADR 0016's own open question,
  unchanged here).
- Per-Swarm partial Bridge removal (disabling one `bridge_swarm_memberships`
  row without a full global credential revocation) — still just a named
  follow-on, not built.
- Editing, renaming, or deleting a Swarm.
- Multi-account self-service beyond the single bootstrap owner.
- A Bridge "leave one Swarm" self-service action initiated from the Host
  side.

## Open questions

- Password-reset or account-recovery, once more than one owner-equivalent
  account exists — moot today, since exactly one owner account is possible
  in this slice.
- Whether `hostapi` should eventually expose the four new read methods for
  `web/`'s benefit once ADR 0013 resolves.
- Everything ADR 0016 and ADR 0017 already left open remains exactly as
  open; this record doesn't narrow either.
