# Phase 0 threat model

**Status:** Draft, pending review
**Scope of Work reference:** Phase 0 deliverable "Draft the threat model, minimum plaintext data map, privacy disclosure, and retention schedule"

---

## Assets

Ordered by what their loss would actually cost.

1. **RomM credentials.** A Client API Token grants access to a member's own
   library. They never leave the Bridge. This is the one asset whose compromise
   the architecture is specifically shaped to prevent.
2. **The central index.** A map of who holds what. No content, but it is the
   single most sensitive thing the Host has, and §10 says so.
3. **Bridge identity keys.** Compromise lets an attacker impersonate a source.
4. **Member content.** Held by members, moved between them, never centralised.
5. **Moderation and audit history.** Its integrity is what makes accountability
   mean anything.

## Adversaries

| Adversary | Capability | What they want |
|---|---|---|
| **Curious member** | Valid credentials, full member API access | Correlate Bridges across Swarms, learn who holds what, see holdings a Swarm's policy excluded |
| **Malicious member** | Valid credentials, runs their own Bridge | Serve corrupted content, inflate contribution metrics, exhaust another member's bandwidth |
| **Compromised Bridge** | An honest member's Bridge, attacker-controlled | Reach that member's RomM server, replay grants, publish false inventory |
| **Network observer** | Sees traffic, not endpoints | Learn who talks to whom and what moves |
| **Compromised Host** | Full database and every alias key | Everything the Host knows. See "Accepted limits". |
| **Compromised relay** | Sees relayed bytes in flight | Content in transit |
| **Outside attacker** | No credentials | Get in |

---

## Threats and responses

### T1. Central credential compromise

*A Host breach exposes every member's RomM server.*

**Response.** Structural, not procedural: the Host never receives a RomM
credential. §3 and §8. There is nothing to leak. This is the single most valuable
property in the design and every other decision defers to it.

### T2. Cross-Swarm correlation by a member

*A member of two Swarms determines that one Bridge is in both, learning holdings
one Swarm chose not to share with them.*

**Response.** Swarm-scoped aliases, HMAC of the Bridge identity under a per-Swarm
key ([ADR 0009](../adr/0009-canonical-identifiers.md)). Per-Swarm publication
filters applied **at the Bridge**, so excluded inventory is never transmitted at
all. Asserted by `TestPhase0_SwarmAliasesDoNotCorrelateAcrossSwarms`.

**Residual.** Timing and inventory-shape correlation. A member seeing two aliases
go offline together, holding suspiciously similar catalogues, can infer a
relationship. Not addressed. Probably not fully addressable.

### T3. Malicious or corrupted content

*A member serves something other than what was requested.*

**Response.** Layered, because any single layer can be argued around:

- Only `verified_eligible` content is advertised or transferable
  (`Manifest.Validate`, `Delta.Validate`).
- The receiving Bridge verifies the canonical payload against the approved
  reference identity **while it is still staged**, before any destination path.
- Transfer acceptance requires SHA-256 agreement, not the catalogue's weaker
  hashes ([ADR 0011](../adr/0011-verification-identity-model.md)).
- A mismatch lands in `verification_conflict`, a visible review state that can
  never reach `source_active`.
- Phase 9 adds hash-mismatch detection, source penalties, and quarantine.

### T4. Path traversal and hostile archives

*A crafted archive escapes the staging directory or exhausts the host.*

**Response.** Applied in Phase 0 rather than deferred to Phase 9, because the
analyser is the first code that ever looks at an attacker-influenced archive:
absolute paths, drive letters, parent traversal, and embedded NULs are rejected;
members are bounded in size; a member whose decompressed length disagrees with
its declaration is refused. Tested in `TestUnsafeArchiveEntriesAreIgnored` and
`TestUnsafeArchiveNames`.

### T5. Partial file published into a library

*A crash mid-transfer leaves a partial or wrong file under its final name, and
RomM indexes it.*

This threat is about crash safety within filesystem publication mode, once it's
enabled. For the broader question of what enabling the mode changes about your
Bridge's trust footprint in the first place — a writable filesystem grant versus
none at all — see
[filesystem-publication-trust.md](filesystem-publication-trust.md).

**Response.** The destination state machine, `protocol/destination.go`. Staging
happens outside RomM's watched tree; verification happens before hand-off;
publication is an atomic rename on the destination filesystem; a cross-filesystem
staging configuration is detected before the transfer starts and cannot be
mistaken for an atomic move. There is no transition from `receiving` to either
destination branch — `verified_in_staging` is unavoidable, and
`TestTransitionRejectsUnknownAndIllegalMoves` asserts it.

### T6. Refresh-token replay

*An interrupted rotation leaves a Bridge unable to authenticate, or lets an
attacker replay a captured token.*

**Response.** Phase 2: current and previous token hashes; a 30 to 60 second,
one-use grace path bound to the same Bridge identity key; reuse detection outside
the grace path with token-family revocation; owner-triggered re-enrolment. The
grace path is bound to the identity key specifically so that a captured token
cannot be replayed from a different Bridge.

### T7. Grant replay and privilege escalation

*A grant is reused after expiry, or by someone it was not issued to.*

**Response.** Short-lived, single-item grants with expiry, byte limits, audience
binding, and replay protection. Identical enforcement on direct and relayed
routes — Phase 6 requires this explicitly, which is why the relay has to verify
grants itself rather than trusting the route.

### T8. Metric manipulation

*A member inflates their standing by shuffling content between their own
Bridges.*

**Response.** Phase 8: dual signed receipts, idempotency, self-dealing exclusion
by failure domain, bounded bandwidth credit, and risk-reduction weighting so that
re-replicating an already resilient item earns little. The failure-domain key from
Phase 3 is what makes self-dealing detectable at all.

### T9. False resilience

*Coverage reports redundancy that does not exist.*

**Response.** `preservation/risk.go`. Only recently revalidated, currently
available replicas in distinct failure domains count. Three machines belonging to
one operator count as one
(`TestPhase0_SameFailureDomainIsNotRedundancy`). Stale claims count as zero
(`TestPhase0_StaleClaimsDoNotCountAsResilience`). This is a correctness property,
not a presentation one: a dashboard that says "resilient" about one operator's
three machines actively discourages the replication that would make it true.

### T10. Resource exhaustion of a member's server

*A member's Bridge saturates their home connection or their RomM instance.*

**Response.** Conservative defaults: one transfer at a time, bandwidth caps,
schedules, backoff. §7 requires avoiding unnecessary load on participating RomM
servers; the ingestion poll interval is deliberately unhurried for the same
reason.

### T11. Relay abuse

*The relay becomes a free bandwidth service or a denial-of-service target.*

**Response.** [ADR 0012](../adr/0012-relay-deployment.md): separate deployment,
no durable storage, byte and time and concurrency limits, revocable grants, and
no path from relay saturation to Host unavailability.

---

## Accepted limits

Stated explicitly. An unstated limit is a limit someone will later assume was
covered.

1. **A compromised Host learns the whole index**, and can correlate every Bridge
   across every Swarm, because it holds every alias key. Mitigated by field
   minimisation, short windows, and store separation. Not eliminated. The
   encrypted-catalogue mode is what would eliminate it, and it is not in the MVP.

2. **A member can always leak content they were legitimately given.** No
   technical control addresses this. It is a membership and moderation matter.

3. **Traffic analysis is not addressed.** An observer who can see both endpoints
   learns that a transfer happened and how large it was.

4. **The architecture is mitigation, not permission.** §10, verbatim. See
   [ADR 0008](../adr/0008-pilot-legal-risk-acceptance.md).

---

## Open questions

- Should the failure-domain key be derived and salted rather than assigned, so a
  Host database leak does not immediately group members by owner?
- Should Bridge identity keys be rotatable without losing a Bridge's history?
  Currently the Bridge id **is** the key digest, so rotation means a new
  identity.
- What is the response to a Bridge that is compromised but still authenticating
  correctly? Quarantine exists; detection does not yet.
