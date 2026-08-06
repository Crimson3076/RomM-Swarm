# RomM Bridge

The local agent. **Phase 1 onward.** Only the RomM adapter boundary
(`bridge/romm`) exists so far, which is Phase 0 work.

Scope of Work §5.2 responsibilities: local RomM authentication, the explicit
API-only or filesystem-publication mode choice, inventory retrieval, the four
identities, manifests and revisions, per-Swarm publication filters, snapshots and
deltas and tombstones, sharing policies and rate limits, grant validation, file
revalidation, serving and receiving, staging and resume and verification, import
through the configured destination mode, ingestion reconciliation, and signed
receipts.

## Boundaries this module must honour

- The RomM Client API Token stays here and is never transmitted to the Host.
- API-only mode requires no RomM library filesystem access at all.
- Filesystem publication is a separate explicit operator mode with a scoped
  writable mount, and refuses to start on an unsafe or cross-filesystem staging
  configuration.
- Canonicalization never rewrites the owner's stored source file.
- An item is not a source until the destination state machine reaches
  `source_active`.
- Credentials never appear in logs. `romm.Client.Redact` is applied at the
  boundary rather than at each call site.

## Blocked on

- ADR 0003. The capability table in `bridge/romm/capability.go` is an assumption
  until a probe has been run against a real RomM instance.
