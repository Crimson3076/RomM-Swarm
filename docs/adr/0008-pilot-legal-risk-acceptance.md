# ADR 0008: Pilot legal risk acceptance

- **Status:** Open
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 gate, Phase 0 deliverable "Complete a documented legal-risk acceptance checkpoint for the private pilot", §10, Phase 10

## Context

Scope of Work §8 includes among the non-goals: *"Treating private membership or
technical architecture as legal permission."* §10 puts it more directly still:
*"architecture is mitigation, not permission."*

This record exists to keep that sentence load-bearing. Every other decision in
this directory is a technical one that a technical person can make. This one is
not, and it should not be dressed up as one.

## Decision

**Open. This decision cannot be made from the codebase and is not made here.**

What this record does is state what has to exist before Phase 10's pilot, so the
gap is visible rather than quietly skipped:

1. **A written risk acceptance** by whoever is accountable for operating the
   Network Host, naming the jurisdiction they are in and the exposure they are
   accepting.
2. **Membership terms** that members agree to, covering what the network is for
   and what members may not do with it.
3. **A statement of operator responsibility**: each Bridge operator is
   responsible for the content on their own server. The network coordinates
   discovery; it does not acquire, host, or distribute content centrally.
4. **A content reporting and takedown procedure**, with a named recipient, a
   response commitment, and a defined outcome — including how a takedown
   propagates to Bridges that hold a copy.
5. **A privacy disclosure** to members, derived from
   [the data map](../phase0/privacy-data-map.md), saying what the Host learns
   about them.

Scope of Work Phase 10 requires "Pre-pilot legal and privacy risk-acceptance
sign-off against the Phase 0 decisions". That sign-off has nothing to sign
against until the five items above exist.

## What the architecture does and does not do

Worth stating precisely, because the temptation to over-claim here is strong.

**It genuinely reduces exposure:**

- No central ROM storage. The Host holds an index, never content.
- No public or anonymous catalogue. Invitation-only, private membership.
- No durable storage on the relay beyond bounded in-flight buffering.
- Content moves directly between the two operators involved.
- Per-Swarm publication filters, so an operator controls what each group sees.

**It does not:**

- Make any of the content lawful to hold or to share.
- Make private membership a defence.
- Make the Host operator not an operator.
- Remove any Bridge operator's responsibility for their own server.

## Consequences

- Phase 10 cannot begin without this, and the phase's own acceptance criteria say
  so.
- Nothing in the codebase should be written in a way that implies legal safety.
  Where documentation describes a mitigation, it should say mitigation.

## Open questions

Every one of them, and none of them are answerable here:

- Which jurisdiction is the Host operated from, and does that choice matter for
  the pilot?
- Who is the accountable operator, and do they accept that role knowingly?
- Is there a takedown recipient who will actually receive and act on notices?
- Does the pilot need legal advice before it starts, rather than after?
