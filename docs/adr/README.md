# Architecture decision records

Scope of Work §13.2: *"Add architecture decision records for every Phase 0 gate
in Section 4."* This directory is that. §4 of the Scope of Work remains the
single source of truth for **which** decisions are open; these records hold the
reasoning, the options considered, and the consequences.

## Status vocabulary

| Status | Meaning |
|---|---|
| **Accepted** | Decided. Code may depend on it. Changing it requires a superseding record. |
| **Proposed** | A concrete recommendation written down so it can be argued with. Code may implement it behind a clearly named default, but nothing may treat it as settled. |
| **Open** | Genuinely undecided. Needs input this project cannot supply from the codebase. |
| **Superseded** | Replaced. Kept for the record; the replacement is named. |

A **Proposed** record is not a soft **Accepted**. Several defaults in the code
(the collection profile, the initial platform set, the revalidation window)
implement a proposal so the harness has something concrete to test against. Each
one names its record, and each record says plainly that the number is a starting
point.

## Records

| # | Title | Phase 0 gate | Status |
|---|---|---|---|
| [0001](0001-implementation-stack.md) | Implementation stack | yes | Accepted |
| [0002](0002-networking-stack.md) | Direct transport and encrypted relay fallback | yes | Open |
| [0003](0003-supported-romm-versions.md) | Supported RomM versions | yes | Open |
| [0004](0004-initial-platforms.md) | Initial platform set | yes | Proposed |
| [0005](0005-default-collection-profile.md) | Default collection profile | yes | Proposed |
| [0006](0006-central-plaintext-and-retention.md) | Central plaintext fields and retention | yes | Proposed |
| [0007](0007-browser-delivery.md) | Browser delivery in the MVP | yes | Proposed |
| [0008](0008-pilot-legal-risk-acceptance.md) | Pilot legal risk acceptance | yes | Open |
| [0009](0009-canonical-identifiers.md) | Canonical identifiers and Swarm-scoped aliases | no | Accepted |
| [0010](0010-event-vocabulary.md) | Event vocabulary and retention classes | no | Accepted |
| [0011](0011-verification-identity-model.md) | Four-identity verification model | no | Accepted |
| [0012](0012-relay-deployment.md) | Relay deployment separation | yes | Proposed |
| [0013](0013-client-packaging.md) | Web frontend and management application packaging | no | Open |

## Phase 0 gate coverage

Every gate in Scope of Work §4 has a record:

- networking stack proving direct plus relay under NAT and CGNAT → 0002
- initial supported RomM major versions → 0003
- initial three to five primarily single-file platforms → 0004
- default collection profile → 0005
- exact central plaintext fields and retention periods → 0006
- browser delivery in the MVP → 0007
- legal risk acceptance, membership terms, takedown procedure → 0008
- whether the relay is operated separately for the pilot → 0012

The remaining §4 items are not Phase 0 gates and may be resolved later:

- final public project name and branding — deferred, no record yet
- coordinator and Bridge implementation language → resolved by 0001
- web frontend framework, desktop packaging → 0013
- leaderboards default versus opt-in per Swarm — deferred to Phase 8
- whether the Host may retrieve approved DAT updates automatically — deferred to Phase 4
- whether a future experimental namespace permits labelled unverified content — deferred, explicitly out of the MVP

## Adding a record

Copy [0000-template.md](0000-template.md). Then update this index, and follow
Scope of Work §12: add the decision to the Decision Log in the Scope of Work,
update any affected phase, and increment the document version.
