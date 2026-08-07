# Phase 0 minimum plaintext data map

**Status:** Draft, pending review
**Scope of Work reference:** §4 gate, §7 Privacy, Phase 0 deliverable "Draft the threat model, minimum plaintext data map, privacy disclosure, and retention schedule"

Scope of Work §10 states the risk this document answers: *"Central database
learns inventories."* The Host is a sensitive map of holdings and activity even
though it never stores a single ROM.

This document lists, field by field, what the Network Host holds in plaintext.
The windows are in [retention-schedule.md](retention-schedule.md). The
plain-language version a member would actually read is
[privacy-disclosure.md](privacy-disclosure.md) — that document is derived from
this one and should never say more than this one supports.

---

## 1. Identity and membership

| Data | Plaintext | Retained | Notes |
|---|---|---|---|
| User id | yes | account lifetime | Random. Carries no information. |
| Display name | yes | account lifetime | Chosen by the user. |
| Email or contact | **only if required for invitations** | account lifetime | Open question: an invitation-only network may not need one at all. If it does, it is the single most identifying field the Host holds. |
| Password verifier | hashed, never plaintext | account lifetime | Argon2id or equivalent. |
| Session tokens | hashed | session lifetime | |
| Swarm id, membership, role | yes | membership lifetime | |
| Bridge id | yes | Bridge lifetime | Derived from the Bridge's public identity key. **Host-internal only.** |
| Bridge public identity key | yes | Bridge lifetime | |
| Bridge refresh token, current and previous | hashed | until rotation plus grace | Phase 2 requires both, for the interrupted-rotation grace path. |
| Per-Swarm alias key | yes | Swarm lifetime | The Host holds these, so **the Host can correlate a Bridge across Swarms.** Members cannot. See §5. |
| Swarm-scoped Bridge alias | derived, not stored | — | Computed from the alias key on demand. |

---

## 2. Inventory

This is the largest and most sensitive store, and it cannot be removed without
removing the product.

| Data | Plaintext | Retained | Notes |
|---|---|---|---|
| File id | yes | while published | Content-derived. Two Bridges holding the same file compute the same id. |
| Canonical digest | yes | while published | The verification target. |
| Reference match, entry name, catalogue version | yes | while published | This is an exact title. It is inventory, not activity — it describes what exists, not what anyone did. |
| Platform, region, revision, size | yes | while published | |
| Holding alias | yes | while published | **Alias, never Bridge id.** |
| Availability, `last_seen_at`, `last_inventory_at`, `last_verified_at` | yes | while published | Three separate timestamps answering three separate questions. |
| Failure-domain key | yes, **Host-internal** | while published | Needed to avoid counting one operator twice. Phase 3 requires it not be exposed to ordinary members, because it groups Bridges by owner. |
| Stored-file digest | yes | while published | Provenance. Reveals *how* a member stores a file. Candidate for removal if it earns nothing. |
| Filesystem paths | **never** | — | The Host has no reason to know where a file sits on someone's disk. |

**Per-Swarm filtering happens at the Bridge.** Inventory excluded by a Swarm's
sharing policy is never transmitted, so it cannot leak from the Host's store. The
Host cannot leak what it was never sent.

---

## 3. Activity: requests, transfers, searches

| Data | Plaintext | Retained | Notes |
|---|---|---|---|
| Access request: requester, source alias, file id, title | yes | **operational window** | Names an exact title. Short window. |
| Grant: id, audience, expiry, byte limit | yes | operational window | |
| Transfer: id, parties, bytes, route, outcome | yes | operational window | |
| Destination state transitions | yes | operational window | |
| Transfer receipts, signed | yes | operational window | After expiry, reduced to title-free aggregates. |
| **Search terms** | **never persisted** | — | Not in an events table, not in an application log. A search is served and forgotten. The surviving aggregate is a count. |
| Aggregate counters: bytes served, items served, completed downloads, coverage snapshots | yes | long term | **Title-free.** Enforced by `TestPhase0_LongTermMetricsAreTitleFree`. |

---

## 4. Security and audit

| Data | Plaintext | Retained | Notes |
|---|---|---|---|
| Authentication failures | yes | security window | |
| Token reuse and family revocation | yes | security window | |
| Grant replay attempts | yes | security window | |
| Rate-limit events | yes | security window | |
| **Raw IP addresses** | yes | **security window only** | Never in permanent history. Never in any aggregate. Shortest window of anything the Host holds. |
| Moderation cases: actor, reason, evidence, time, duration, outcome | yes | long term, immutable | Phase 9 requires every field. Names actors and actions, never search terms or library contents. |
| Administrative changes | yes | long term, immutable | |

---

## 5. What the Host unavoidably knows

Stated plainly. A privacy disclosure that omitted this would be dishonest, and
the temptation to omit it is exactly why it is a numbered section.

1. **Which aliases hold which verified files, per Swarm.** This is the catalogue.
   The product does not exist without it.
2. **Which alias belongs to which Bridge, and which Bridge to which user.** The
   Host holds every alias key. Members cannot correlate a Bridge across Swarms;
   **the Host can.**
3. **Which Bridges share a failure domain**, since that is what independent
   replica counting requires.
4. **Recent activity**, for the length of the operational window.

The Swarm-scoped alias mechanism protects members from each other. It does not
protect members from the Host, and it is never described as doing so. The later
privacy-first encrypted catalogue mode (§7) is what addresses point 1; until it
exists, point 1 stands.

---

## 6. What the Host never holds

- RomM passwords. §3, non-negotiable.
- RomM Client API Tokens. They stay on the Bridge.
- ROM content, at any point, in any form. §8.
- Filesystem paths from any member's server.
- Search terms.
- Any content on the relay beyond bounded in-flight buffering. §5.6.

---

## Open questions

- Is an email address needed at all? An invitation-only network may be able to
  operate without one, which would remove the most identifying field here.
- Does the stored-file digest earn its place? It is provenance, and provenance is
  useful, but it also describes how a member stores their library.
- Should the failure-domain key be a salted derivation rather than an assigned
  value, so that a Host database leak does not immediately group members by
  owner?
