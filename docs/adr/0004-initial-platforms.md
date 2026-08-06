# ADR 0004: Initial platform set

- **Status:** Proposed
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 gate, Phase 0 deliverable "Select three to five initial, primarily single-file platforms"

## Context

Scope of Work §10 lists "Multi-file import limitations" as a major risk and
answers it by keeping the MVP platform set primarily single-file, gating disc
support behind later adapter proof. §8 places multi-file and disc-platform
transfer outside the MVP entirely.

The set has to be small enough that every platform gets a real, tested
canonicalization adapter, and varied enough that the adapter architecture is
actually exercised rather than being five copies of "hash the file".

## Decision

Adopt the candidate set named in the Scope of Work:

| Platform | Key | Canonicalization | Why it is in the set |
|---|---|---|---|
| Game Boy | `gb` | identity | Baseline. Small, ubiquitous, headerless. |
| Game Boy Color | `gbc` | identity | Forces platform assignment from the cartridge's own CGB flag rather than from the library folder. |
| Game Boy Advance | `gba` | identity | Larger files; header validation by computed checksum rather than by a constant. |
| Nintendo DS | `nds` | trimmed-dump reconstruction | The trimmed-dump case. Canonicalization genuinely produces different bytes from those on disk. |
| Mega Drive / Genesis | `genesis` | copier-header removal and de-interleaving | The case a single raw-file hash rule cannot handle at all. |

Implemented as `protocol.InitialPlatforms()`, with adapters in `verify/` and
fixtures in `internal/romfixture`.

### Why these five and not five easier ones

Three of them are identity transforms, which is honest — most cartridge platforms
are. But two are not, and they were kept specifically because they break naive
approaches:

- **Genesis** circulates as both `.bin` and `.smd`. The same game in the two
  encodings shares no bytes in common order. Any design that hashes the stored
  file reports them as two unrelated holdings. This is the concrete instance of
  the §10 risk "Real files differ from reference encoding", and it is proven
  handled by `TestPhase0_GenesisSMDAndBinCanonicalizeIdentically`.

- **Nintendo DS** trimmed dumps require reconstructing padding to match the
  reference. That forces the architecture to separate the stored identity from
  the canonical identity, and forces the rule that the owner's file is never
  rewritten. A set without it would have let the two identities stay collapsed
  and the flaw would have surfaced in Phase 6 instead.

## Options considered

### The five above — chosen

Covers identity, header removal, byte de-interleaving, and padding
reconstruction. Every one is primarily single-file.

### A smaller set: GB, GBC, GBA

Three platforms, all identity transforms, fastest to Phase 1. Rejected: it would
prove the pipeline against the easiest possible case and defer every real
canonicalization question to a phase where changing the data model is expensive.

### Including SNES

SNES has copier headers, which is a genuine case. Rejected for now only because
Genesis already covers header removal *and* adds de-interleaving, so SNES would
be a second instance of an already-covered class rather than a new one. It is the
natural sixth platform.

### Including a disc platform

Rejected outright, per Scope of Work §8 and Phase 4, which require a disc adapter
contract covering track layout and CHD internal verification before any disc
content is transferred. Nothing here attempts that.

## Consequences

- `verify.DefaultRegistry()` registers exactly six adapters for these five
  platforms (Genesis needs two: plain and copier format).
- `TestPhase0_EveryInitialPlatformIsRecognised` fails if a platform is added to
  `protocol.InitialPlatforms()` without a fixture, so the set and its evidence
  cannot drift apart.
- Reference catalogues must be locked for exactly these five (see ADR 0005 and
  the Phase 0 deliverable "Lock the approved reference catalog and
  collection-profile rules for each initial platform").

## Open questions

- Is Mega Drive the right fifth, or is SNES more useful to the pilot's actual
  members? The adapter work is comparable; the question is which libraries the
  pilot operators actually hold.
- Should GB and GBC count as one platform or two for coverage reporting? They are
  separate No-Intro sets, which is why they are separate here.
