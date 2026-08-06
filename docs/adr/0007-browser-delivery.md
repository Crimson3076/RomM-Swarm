# ADR 0007: Browser delivery in the MVP

- **Status:** Proposed
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 gate ("whether browser delivery is in the MVP or the browser may only send work to a Bridge"), §5.3, Phase 5, Phase 6

## Context

Scope of Work §5.3 lists both "Browser download requests" and "Sending downloads
to a user-owned Bridge" as web frontend responsibilities, and §4 asks which of
them is in the MVP.

The two are architecturally very different:

- **Send to a Bridge**: the browser creates an intent. The user's own Bridge
  performs the transfer, using the transport, grant, resume, verification, and
  destination state machine that Phase 6 already builds. The browser holds no
  content and needs no new path.

- **Browser delivery**: the browser is a transfer endpoint. Content has to reach
  it. The Bridge-to-Bridge transport is not reachable from a browser, so this
  needs either a relay path that terminates in the browser or a Host-mediated
  stream — and the Host streaming ROM content is uncomfortably close to
  "durable ROM storage on the Network Host", which §8 lists as a non-goal, and it
  puts content through the one component the whole design keeps content away
  from.

Browser delivery also cannot honour Phase 6's guarantees. There is no staging
directory, no `.part` file, no atomic publication, and no RomM ingestion
reconciliation. A file delivered to a browser is never a source, never counts
toward coverage, and cannot be verified into the destination state machine.

## Decision

**Proposed: the browser may only send work to a Bridge. Browser delivery is out
of the MVP.**

The web frontend gets:

- Catalogue search, comparison, and preservation views (Phase 5).
- "Send this to my Bridge", which creates a request the user's own Bridge
  fulfils.
- Visibility into the resulting transfer's state, including the destination state
  machine.

It does not get a download button that streams bytes to the browser.

## Options considered

### Send to a Bridge only — proposed

Reuses everything Phase 6 builds. No new transport, no new trust boundary, no
content through the Host. The user experience is "I asked for it, it appeared in
my library", which is arguably the better outcome anyway. Cost: a user without a
Bridge cannot obtain anything, which makes the Bridge mandatory for participation
rather than optional.

### Browser delivery through a Host-mediated stream

Users without a Bridge can participate. Costs: content transits the Host;
verification, resume, and grant semantics differ from every other path, breaking
Phase 6's requirement of identical behaviour across routes; the Host becomes a
bandwidth and abuse target; and it sits awkwardly against the project's stated
posture that the Host never holds content.

### Browser delivery through the relay

Content avoids the Host but the relay gains a browser-facing termination, which
is a new authentication surface and a new abuse target on a component whose whole
job is to be minimal.

## Consequences

- Every member needs a Bridge to receive anything. This should be stated plainly
  in onboarding rather than discovered.
- The web frontend has no content path at all, which materially reduces its
  security surface and its legal exposure.
- If browser delivery is later wanted, it is an additive path, not a change to
  an existing one.

## Open questions

- Is "you must run a Bridge to receive anything" acceptable to the pilot? It
  raises the participation bar noticeably.
- Should a user be able to send work to *another* user's Bridge with permission,
  for example to help someone less technical? That is a sharing-policy question
  rather than a delivery one.
