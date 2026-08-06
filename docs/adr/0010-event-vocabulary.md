# ADR 0010: Event vocabulary and retention classes

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-06
- **Scope of Work reference:** Phase 0 deliverable "Define the minimum event vocabulary"; §7 Privacy; Phase 9

## Context

Scope of Work §7 makes several privacy promises that are easy to state and hard
to keep:

- Exact request and transfer titles use short, documented retention.
- Long-term metrics are aggregate and title-free.
- Search terms are excluded from ordinary logs.
- Raw IP addresses use short security retention only.

A promise like that, written only in a document, decays. Someone adds a useful
new event, it carries a game name, and it lands in a table nobody has classified.
Six months later the retention sweeper is deleting three of the four places
titles are stored.

## Decision

The event vocabulary is **closed**, and every kind declares a retention class at
the point it is defined, in `protocol/events.go`.

### Retention classes

| Class | Window | May name a title | Contents |
|---|---|---|---|
| `operational` | short, see the retention schedule | yes | Transfers, requests, inventory, grants |
| `security` | short, separately documented | yes | Auth failures, token reuse, grant replay, verification conflicts. May carry an IP address. |
| `aggregate` | long term | **no** | Coverage snapshots, counts, byte totals |
| `audit` | long term, immutable | **no** | Moderation and administrative history |

### Enforced properties

Three tests turn the promises into checks:

- `TestPhase0_EveryEventKindDeclaresRetention` — a kind cannot exist without a
  retention class and a human summary.
- `TestPhase0_LongTermMetricsAreTitleFree` — nothing kept long term may be marked
  as naming a title.
- `TestTitleBearingEventsAreShortRetention` — anything that can name a title must
  be in a short-window class, so the sweeper has something to sweep.

### The envelope has no free-form message field

Deliberately. A free-form field is where search terms, file paths, and IP
addresses end up by accident, and it defeats the classification above entirely.
Detail belongs in typed fields on the specific payload.

### Unknown kinds are rejected

The Host refuses to store an event kind it does not know. A Bridge running ahead
of the Host would otherwise write events the Host's retention sweeper does not
understand, and would therefore never delete.

## Consequences

- Adding an event is a deliberate act with a privacy decision attached.
- The retention sweeper can be written generically: it walks the vocabulary
  rather than a hand-maintained list of tables.
- `ActorBridge` on the envelope is a global Bridge id, which is Host-internal and
  audit-facing. Member-facing APIs must project it to a Swarm-scoped alias; that
  projection is a Phase 2 and Phase 3 obligation, not something this record
  provides.

## Open questions

- The exact windows are in ADR 0006 and the retention schedule, both still
  Proposed.
- Should `security` events carrying an IP address be a fifth class with its own
  shorter window? Currently they share `security`, which means the window is set
  by the most sensitive thing in the class.
