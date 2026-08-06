# Transfer relay

Encrypted fallback transport for Bridges that cannot reach each other directly.
**Phase 6 — no implementation yet.**

Scope of Work §5.6 responsibilities: encrypted fallback transport, authentication
of short-lived transfer grants, byte and time and concurrency and bandwidth
limits, no durable ROM storage beyond bounded in-flight buffering, operational
isolation from the Host, and usage receipts that do not retain exact title names
long term.

## Boundaries this module must honour

- No import of Host internals. See docs/adr/0012-relay-deployment.md.
- No RomM credential, ever. No Host database credential.
- Grants are verified from the grant and the Host's public verification key.
  Calling back into the Host on the hot path would make the Host a relay
  dependency, which defeats the separation.
- Nothing on the Host's critical path may depend on the relay being available.

## Blocked on

- ADR 0002. The transport decision may make this a DERP server rather than
  something written here.
