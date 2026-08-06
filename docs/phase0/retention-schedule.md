# Phase 0 retention schedule

**Status:** Draft, pending review
**Scope of Work reference:** §4 gate, §7 Privacy, Phase 9

The windows below are **proposals**. They are written as concrete numbers rather
than as "short" so that they can be argued with; every one of them is a starting
point, and the reasoning is given so a different number can be chosen for a
stated reason.

Retention is enforced by class, and the class is declared on each event kind in
`protocol/events.go`. See [ADR 0010](../adr/0010-event-vocabulary.md).

---

## Windows

| Class | Proposed window | Contains | On expiry |
|---|---|---|---|
| `operational` | **30 days** | Transfers, requests, grants, inventory events, destination state changes. May name an exact title. | Reduced to title-free aggregates, then deleted. |
| `security` | **7 days** | Authentication failures, token reuse, grant replay, rate limiting, verification conflicts. May contain a raw IP address. | Deleted. Counts survive as aggregates. |
| `aggregate` | **indefinite** | Counts, byte totals, coverage and resilience snapshots. **Never a title.** | Retained. |
| `audit` | **indefinite, immutable** | Moderation and administrative actions. Names actors and actions. **Never a title, never a search term.** | Retained. |

### Why 30 days for operational

Long enough to debug a transfer that failed two weekends ago, which is the actual
support case. Short enough that the Host is not a durable record of what everyone
downloaded, which is the actual risk. The failure mode of a longer window is that
the Host becomes exactly the thing §10 warns about; the failure mode of a shorter
one is a support conversation that has to end in "I can't tell you why that
failed".

### Why 7 days for security

Security records are the only ones that carry raw IP addresses, so this is the
shortest window. Seven days covers a credential-stuffing attempt or a token-reuse
incident being noticed and investigated. Beyond that, the count matters and the
address does not.

### Why aggregates are indefinite

Because they carry nothing to protect. A coverage snapshot is a number of
verified files on a date. `TestPhase0_LongTermMetricsAreTitleFree` fails the build
if anything title-bearing is ever classified as an aggregate, so this is a
property of the code rather than a policy that decays.

---

## Never retained

| Data | Rule |
|---|---|
| Search terms | Never persisted anywhere, including application logs. Only a count survives. |
| RomM credentials | Never transmitted to the Host at all. |
| ROM content | Never on the Host. On the relay, bounded in-flight buffering only. |
| Raw IP addresses in aggregates | Never. Security class only. |
| Exact titles in long-term storage | Never. Enforced by test. |

---

## The sweeper

The retention sweeper walks the **event vocabulary**, not a hand-maintained list
of tables. This matters: a list of tables goes stale the first time someone adds
a table and forgets to update it, and the failure is silent — data simply stops
being deleted and nobody notices.

Because `protocol.EventKind.Retention()` returns an error for an unknown kind,
and because the Host rejects unknown kinds at write time, an event that the
sweeper does not understand cannot be stored in the first place.

### Required properties

1. Idempotent. Running twice deletes nothing extra.
2. Aggregates are computed **before** the detail behind them is deleted, in the
   same transaction, so a sweep interrupted halfway does not lose the count as
   well as the detail.
3. Every sweep records an audit event: what class, what window, how many rows.
4. A sweep that cannot complete alerts rather than retrying silently. Retention
   failing quietly is the same as having no retention.

---

## Backups

Backups are where retention schedules usually go to die: data deleted from the
live database survives for as long as the oldest backup.

**Proposed:** backup retention must not exceed the longest short-window class it
contains. Either backups expire within 30 days, or backups exclude the
short-window tables and cover only identity, inventory, aggregates, and audit.

The second is probably right — restoring a month of transfer history is not worth
much, and the tables that matter for recovery are the long-lived ones.

---

## Open questions

- Do moderators need exact titles from a completed transfer beyond 30 days? If
  they do, the operational window is a moderation constraint as well as a privacy
  one, and the two need reconciling rather than one quietly overriding the other.
- Should a member be able to request early deletion of their own operational
  records? Technically easy, and it interacts with moderation.
- Does the pilot's jurisdiction impose anything here? A question for
  [ADR 0008](../adr/0008-pilot-legal-risk-acceptance.md).
