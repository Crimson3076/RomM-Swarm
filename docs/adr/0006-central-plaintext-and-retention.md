# ADR 0006: Central plaintext fields and retention

- **Status:** Proposed
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 gate ("exact central plaintext fields and retention periods for identity, inventory, requests, transfers, searches, audit events, and IP addresses"), §7 Privacy, Phase 9

## Context

Scope of Work §10 states the problem plainly: "Central database learns
inventories" — the Host is "a sensitive map of holdings and activity even
without storing files". The decision log records "Minimize central plaintext data
from the first release" as accepted.

So the question is not whether to minimise but exactly what the Host holds, in
plaintext, for how long. That has to be settled in Phase 0 because it constrains
the Phase 2 schema, and retrofitting minimisation onto a schema designed without
it is close to a rewrite.

## Decision

**Proposed**, pending review. The concrete field-by-field map is in
[docs/phase0/privacy-data-map.md](../phase0/privacy-data-map.md) and the windows
are in [docs/phase0/retention-schedule.md](../phase0/retention-schedule.md). The
governing rules:

1. **Four retention classes, declared per event kind**, enforced in
   `protocol/events.go`. See ADR 0010.

2. **Exact titles live only in short-window classes.** Long-term aggregates carry
   counts and byte totals, never a title. Tested.

3. **Search terms are never persisted.** Not in an events table, not in an
   application log. A search is served and forgotten. The aggregate that survives
   is a count of searches, which is what capacity planning actually needs.

4. **Raw IP addresses are security-class only**, with the shortest window of any
   class, and never enter permanent history or any aggregate.

5. **Member-facing surfaces carry Swarm-scoped aliases, never global Bridge
   identifiers.** ADR 0009.

6. **Identity, inventory, and short-lived request or transfer data are separated**
   where practical, so that a compromise of one store is not automatically a
   complete picture of who holds what. Phase 9 deliverable.

### What the Host unavoidably knows

Stated plainly, because a privacy disclosure that omits this would be dishonest:

- Which aliases hold which verified files, per Swarm. This is the catalogue; the
  system does not function without it.
- Which alias belongs to which Bridge and which user. The Host holds every alias
  key, so the Host **can** correlate a Bridge across Swarms. Members cannot.
- Recent transfer activity, for the length of the operational window.

Removing the first of these is what the later privacy-first encrypted catalogue
mode is for. Nothing in the MVP should be described as preventing it.

## Consequences

- The Phase 2 schema is designed against the data map, not the other way round.
- The retention sweeper walks the event vocabulary, so it cannot miss a table
  someone forgot to list.
- The privacy disclosure shown to members can be generated from the same map,
  which keeps the promise and the implementation from drifting apart.

## Open questions

These need an answer from the pilot operators, not from the codebase:

- How long is "short" for operational records? The schedule proposes 30 days.
  Long enough to debug a failed transfer, short enough that the Host is not a
  durable record of what everyone downloaded.
- How long for security records with IP addresses? The schedule proposes 7 days.
- Do moderators need to see exact titles in a completed transfer after the
  operational window? If they do, the window is a moderation constraint and not
  only a privacy one, and the two need reconciling.
- Is any of this subject to a jurisdiction's data-protection regime for the
  pilot? That is a question for ADR 0008.
