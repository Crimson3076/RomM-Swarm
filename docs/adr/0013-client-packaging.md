# ADR 0013: Web frontend and management application packaging

- **Status:** Open
- **Phase 0 gate:** no
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 ("web frontend framework", "separate desktop application, packaged web application, or both"), §5.3, §5.4, Phase 5, Phase 7

## Context

Scope of Work §5.3 and §5.4 describe two clients with genuinely different jobs:

- The **web frontend** is for discovery, comparison, preservation health, and
  administration. Mostly reads, occasional writes, no long-running work.
- The **management application** is for bulk transfer workflows: hundreds of
  checked selections that survive navigation, Ctrl and Shift multi-selection that
  adds and removes predictably, queue reordering, per-transfer progress, and a
  persistent completion ledger across application restarts.

The second is where the requirements bite. Phase 7 acceptance requires that "A
user can select hundreds of items without selection state being lost" and that
"Closing and reopening the application preserves queue and completion state".
That is a stateful desktop application's problem statement, and it is the kind of
interaction that browser tabs handle badly.

## Decision

**Open.** Deferred until Phase 5 begins. Nothing in Phases 0 through 4 depends on
it, and §3 already fixes the constraint that matters: the web frontend and the
management application use the same role-scoped API, and neither gets direct
database access.

That constraint is what makes deferring safe. The API is designed against
requirements, not against a chosen framework, so the client decision cannot leak
backwards into the Host.

## What should drive the decision when it is made

1. **Does the queue live in the client or on the Bridge?** This is the real
   question, and it is not a UI question. If the transfer queue is Bridge-side
   state that clients merely view and manipulate, then "closing and reopening
   preserves queue state" is free, and the management application can be a
   packaged web application. If the queue lives in the client, it needs durable
   local storage and the desktop path becomes much more attractive.

   The Bridge-side answer is probably right: the Bridge is already the thing that
   survives restarts, already owns the destination state machine, and already has
   durable storage. A queue that only exists while an application window is open
   is a queue that loses work.

2. **Selection state.** Hundreds of persistent checked selections, independent of
   row highlighting, with predictable Ctrl and Shift behaviour. Achievable in a
   browser but easy to get wrong; worth prototyping before committing.

3. **Cross-platform reach.** Phase 7 targets Linux desktop initially. A packaged
   web application reaches everywhere for free; a native shell does not.

4. **Distribution weight.** The same argument that decided ADR 0001 applies
   again: whatever is shipped to users is a support burden.

## Consequences

- The role-scoped API must be designed as though the queue is Bridge-side, since
  that is the likelier answer and it is the harder one to retrofit.
- No frontend code should be written before this is decided, and none has been.

## Open questions

- Framework for the web frontend. Genuinely open; it should follow from the
  queue-location decision rather than lead it.
- One client or two? A packaged web application that is *also* the web frontend,
  with the bulk workflows behind a mode, is a real option and would halve the
  surface to maintain.
