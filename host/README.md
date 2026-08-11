# Network Host

Central coordination service. **Phase 2 onward.** Slice 1 — accounts,
Swarms, memberships, invitations, and Bridge enrolment/revocation — is
implemented; see [ADR 0016](../docs/adr/0016-network-host-identity-slice.md)
for exactly what that slice does and does not cover. Everything else listed
below (inventory index, catalogue distribution, search, sharing policies and
grants, receipts, preservation requests, announcements, leaderboards,
moderation) remains unimplemented.

Scope of Work §5.1 responsibilities: accounts, roles, Swarms, memberships,
invitations, Bridge enrolment and revocation, Swarm-scoped aliases, the central
inventory index and change feed, reference catalogue versioning, search and
completion calculations, sharing policies and transfer grants, receipts and
aggregate metrics, preservation requests, announcements, leaderboards,
moderation cases, and the role-scoped APIs.

## Boundaries this module must honour

- It never receives a RomM credential. Scope of Work §3, non-negotiable.
- It never stores ROM content. §8.
- Member-facing APIs carry Swarm-scoped `BridgeAlias` values, never a global
  `BridgeID`. See docs/adr/0009-canonical-identifiers.md.
- The failure-domain key is Host-internal and must not reach ordinary members.
- Every stored event must be a known `protocol.EventKind`, so the retention
  sweeper cannot be handed data it does not understand.
- The schema follows docs/phase0/privacy-data-map.md, not the other way round.

## Blocked on

- ADR 0006, the plaintext and retention decisions the schema depends on —
  blocks inventory, activity, and security-event storage (the parts of Host
  beyond slice 1) but not identity/membership, whose fields are already
  fully specified in the data map.

ADR 0003 (supported RomM versions) does not actually block anything here —
it governs the Bridge-to-RomM capability contract, which Host never touches
(Host never receives a RomM credential). Removed from this list; it was
never a real dependency.
