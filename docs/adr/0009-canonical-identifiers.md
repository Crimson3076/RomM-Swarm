# ADR 0009: Canonical identifiers and Swarm-scoped aliases

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-06
- **Scope of Work reference:** Phase 0 deliverables "Define canonical identifiers for Bridge, user, Swarm, game, exact file, transfer, and inventory revision" and "Define Swarm-scoped Bridge aliases"; Phase 2 acceptance

## Context

Identifiers look like a naming convention and are actually a privacy mechanism.
Scope of Work §3 requires that each Swarm sees a Swarm-scoped alias rather than a
globally correlatable Bridge identifier, and Phase 2 acceptance requires that
"Membership in two Swarms does not expose one Swarm's Bridge alias to the other".

At the same time, the central index only works if two Bridges that have never
communicated agree on what "the same file" means.

Those two requirements pull in opposite directions, and the resolution is that
different identifier kinds are constructed differently.

## Decision

All identifiers are `prefix_` plus 26 characters of Crockford base32 (no I, L, O,
or U, so an identifier read aloud does not become a different identifier).

| Kind | Prefix | Construction | Why |
|---|---|---|---|
| User | `usr` | random | Must carry no information. |
| Swarm | `swm` | random | Must carry no information. |
| Transfer | `xfr` | random | Must carry no information. |
| Grant | `grt` | random | Must carry no information. |
| Bridge | `brg` | SHA-256 of the public identity key | Re-enrolment with the same key is recognisable rather than creating a silent second Bridge, and the identifier cannot be claimed without the private key. |
| Exact file | `fil` | SHA-256 of the **canonical payload** | Two Bridges independently holding the same file compute the same identifier without communicating. This is what makes replica counting possible. |
| Canonical game | `gam` | catalogue family plus canonical title key | Stable across reference-set versions, so importing a newer DAT does not renumber the index. |
| Swarm-scoped alias | `sba` | HMAC-SHA-256 of the Bridge id under a per-Swarm key | Stable within a Swarm, uncorrelatable across Swarms. |

Inventory revisions are a monotonic `uint64` per Bridge per Swarm, starting at 1.
Revision 0 means "nothing published yet".

### Derivation details that matter

- **Domain separation.** Every derived kind mixes a distinct domain tag, so a
  file digest can never be replayed as a Bridge identity.
- **Length prefixing.** Multi-part derivations length-prefix each part, so
  `("ab","c")` and `("a","bc")` cannot collide.
- **The alias mixes the Swarm id as well as using a per-Swarm key.** If an alias
  key were ever accidentally reused across two Swarms, the aliases still differ.
  Defence against an operational mistake, not a cryptographic one.
- **The file identifier keys on the canonical payload, not the stored file.** A
  Bridge holding a game as a bare ROM and one holding it zipped must agree that
  they hold the same thing.

## Consequences

- The Host can correlate a Bridge across Swarms, because it holds every alias
  key. This is a known and documented limit, recorded in the threat model. The
  later encrypted-catalogue mode is what addresses it; the alias mechanism is not
  claimed to.
- Member-facing and catalogue APIs must project `BridgeID` to `BridgeAlias`. The
  `Event` envelope keeps `ActorBridge` as a global id precisely because it is
  Host-internal and audit-facing; anything member-facing must not carry it.
- Identifier validation is enforced at decode time, not only by the type system,
  because JSON decoding bypasses the type system.

## Open questions

- Should aliases rotate? A stable alias lets members build reputation with a
  source, and also lets them track one. Currently stable; rotation would need a
  reputation-carrying mechanism to replace it.
