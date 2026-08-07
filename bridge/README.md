# RomM Bridge

The local agent. **Phase 1 onward assembles these into one running service; the
pieces below are Phase 0 work**, each provable on its own without a live RomM
instance or a second Bridge to talk to.

Scope of Work §5.2 responsibilities: local RomM authentication, the explicit
API-only or filesystem-publication mode choice, inventory retrieval, the four
identities, manifests and revisions, per-Swarm publication filters, snapshots and
deltas and tombstones, sharing policies and rate limits, grant validation, file
revalidation, serving and receiving, staging and resume and verification, import
through the configured destination mode, ingestion reconciliation, and signed
receipts.

| Package | Responsibility |
|---|---|
| `bridge/romm` | The RomM API adapter boundary: capability probing, credential presentation, download/upload building blocks. |
| `bridge/scan` | Turns one RomM server's inventory into a normalized local manifest: list, download, four-identity analysis, classification. |
| `bridge/publish` | Per-Swarm sharing policy evaluation and filtered manifest/delta production from a set of local items. |
| `bridge/destination` | Staging, the same-filesystem preflight, and atomic no-overwrite filesystem publication. |
| `bridge/ingest` | The receiving flow: staging through verification through RomM ingestion reconciliation to `source_active`. |

Not yet assembled into a single running Bridge process — that wiring, plus
local persistence for the manifest/scan state and the auth credential store, is
Phase 1.

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
- No package outside `bridge/romm` guesses a RomM URL shape independently.
  `bridge/scan` builds every request from a `romm.Report`'s already-resolved
  capability paths, so there is exactly one place a wrong assumption about
  RomM's API needs correcting.

## Blocked on

- ADR 0003. The capability table in `bridge/romm/capability.go` is an assumption
  until a probe has been run against a real RomM instance.
