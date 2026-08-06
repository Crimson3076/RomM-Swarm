# Web frontend

Member portal. **Phase 5 — no implementation yet, and no framework chosen.**

Scope of Work §5.3 responsibilities: authentication, announcements, catalogue
search and filtering, local-versus-Swarm comparison, platform completion and
preservation risk, preservation requests, sending work to a user-owned Bridge,
activity and optional leaderboards, Bridge and sharing-policy management, and
role-appropriate moderation and administration.

## Boundaries this module must honour

- The same role-scoped API as every other client. No direct database access.
  Scope of Work §3.
- Known inventory and currently available inventory are presented as separate
  facts, never collapsed. Phase 5 acceptance.
- Stale or same-operator replicas are never presented as independent resilience.
  Phase 5 acceptance.
- Content coverage is never presented as proof a platform is playable, because
  BIOS material is not shared. Phase 4 acceptance.

## Blocked on

- ADR 0013, framework and packaging. The question that decides it is whether the
  transfer queue lives in the client or on the Bridge.
- ADR 0007 proposes that the browser may only send work to a Bridge, with no
  browser content delivery in the MVP.
