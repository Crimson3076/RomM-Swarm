# ADR 0011: Four-identity verification model

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-06
- **Scope of Work reference:** §3, Phase 0 deliverables "Build a verification spike…that distinguishes stored-file, container, canonical-payload, and reference identities" and "Define the rule that canonicalization never rewrites the user's stored source file"; Phase 4

## Context

Scope of Work §10 names the risk directly: "Real files differ from reference
encoding", with the response "Versioned platform adapters for archives, headers,
byte order, trimming, and future disc formats; never use a universal raw-file
hash rule".

The reason a universal rule cannot work is concrete. The same Mega Drive game
circulates as a plain image and as a Super Magic Drive copier image; the two
share no bytes in common order. A trimmed Nintendo DS dump is the same cartridge
as its full dump with the padding removed. A zipped ROM and a bare ROM are the
same game. Under a single stored-file hash, each pair is two unrelated holdings.

## Decision

Every held file has four identities, computed and stored separately.

| Identity | What it is | Used for | Never used for |
|---|---|---|---|
| **Stored file** | Hash of the bytes on the owner's disk | Provenance, local deduplication, detecting on-disk change | Reference matching |
| **Container** | Archive format and member list | Provenance | Matching — two zips of one ROM differ |
| **Canonical payload** | Hash after format-aware normalisation | Reference matching, transfer verification, the exact-file identifier | — |
| **Reference** | What the approved catalogue says a correct dump hashes to | The verification target | — |

### Rules

1. **Canonicalization never rewrites the source.** Adapters take an
   `io.ReaderAt` and return a stream. Nothing in `verify/` opens a file for
   writing. Asserted by `TestPhase0_CanonicalizationNeverMutatesTheSource`
   against a real file on disk, including its modification time.

2. **Adapters are versioned, and every result names its adapter.** Phase 4
   acceptance requires this. Changing an adapter's behaviour means incrementing
   its version, never editing the old rule in place — otherwise a rule change
   silently reclassifies history and nobody can tell which figures were computed
   under which rules.

3. **Header removal only where the reference rules define it.** The Genesis
   copier header is removed because the No-Intro set defines the canonical image
   as headerless. This is not a general licence to strip leading bytes.

4. **Structural evidence outranks the library's platform assignment.** RomM's
   platform comes from the library layout, which is a human convention and is
   wrong often enough that trusting it would defeat the purpose. When they
   disagree, the structure wins and the disagreement is recorded as a note.

5. **A catalogue match and a transfer acceptance are different bars.** A
   reference match may rest on the catalogue's strongest available hash, which is
   usually SHA-1. Accepting bytes from a partially trusted peer requires SHA-256.
   `protocol.StrengthLevel` makes the distinction explicit rather than implied.

6. **Any active disagreement is fatal.** If SHA-1 agrees but CRC32 does not,
   there is no match. Anything else lets a crafted or corrupted record paper over
   a mismatch.

### Classification

Evidence becomes permission in exactly one place, `reference.Classify`:

- `unmatched` — no canonical payload, or no reference entry, and no metadata claim
- `matched_unverified` — the library names a game, the hash matches nothing
- `verified_excluded` — matches a reference entry the active profile excludes
- `verified_eligible` — matches an included entry; **the only publishable class**
- `conflict` — the evidence contradicts itself; needs review

## Consequences

- `protocol.Item.Validate()` refuses an item whose `FileID` does not derive from
  its canonical digest, and refuses a publishable item with no reference match or
  no adapter reference. A hand-assembled or corrupted item is rejected at the
  boundary.
- `Manifest.Validate()` and `Delta.Validate()` refuse to publish anything that is
  not `verified_eligible`, so §3's central rule is enforced by the wire types
  rather than by discipline.
- A trimmed dump can still be served: the sender streams the same reconstruction
  the receiver verifies against. Stored and canonical bytes differing is normal,
  not an error state.
- Adding a platform means adding an adapter and a fixture. The registry refuses
  duplicate adapter references, so two rule sets cannot claim one identity.

## Open questions

- Disc platforms need an adapter contract covering track layout and CHD internal
  verification, without claiming raw Redump equality from a CHD file hash. Phase 4
  gates this; nothing here attempts it.
- The DS padding byte is a guess (0xFF). A cartridge padded with 0x00 will not
  match and stays `matched_unverified`, which is honest but leaves genuine
  holdings unverified. Trying both padding bytes is cheap and probably worth
  doing.
