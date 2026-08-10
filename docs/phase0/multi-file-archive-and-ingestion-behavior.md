# Multi-file, archive, and RomM ingestion behavior

**Status:** The archive and multi-file sections are confirmed against this
codebase's own tests. The RomM ingestion section was an assumption when this
document was first drafted; most of it is now confirmed against a live RomM
5.0.0 instance, with the remaining unconfirmed pieces named explicitly.
**Scope of Work reference:** Phase 0 deliverable "Document current multi-file,
archive, and RomM ingestion behavior"

Two different kinds of "current behavior" are documented here, and they carry
different weight. The archive and multi-file sections describe **this
codebase's own code** — `verify/analyze.go` — and every claim names the test
that would fail if the claim stopped being true. The RomM ingestion section
describes **RomM's** behavior; where it's confirmed, it names the specific
evidence (server log lines, response bodies) rather than a test, since the
server itself isn't part of this repository. Where it's still an assumption,
that's stated as plainly as it was before.

---

## Archive behavior (confirmed)

### What's recognized

Exactly one container format: **zip**, detected by its four-byte magic number
(`PK\x03\x04`) at the start of the file, in `Analyzer.unwrap`. Anything else —
7z, RAR, tar, gzip — is not opened as a container at all; it's analyzed as a
bare file, which means a platform adapter has to recognize its raw bytes
directly or the item is `unmatched`.

This is a real gap, not an oversight: those formats simply aren't implemented
yet. Nothing about the architecture assumes zip specifically — `ContainerKind`
in `protocol/manifest.go` is already an enum precisely so a second container
kind can be added — but until one is, a RAR'd ROM behaves exactly like an
unrecognized file.

### What happens inside a zip

1. The archive is opened and every entry is walked.
2. Directory entries are skipped.
3. An entry with an unsafe name is **ignored**, not fatal to the rest of the
   archive: absolute paths, drive letters, `..` traversal, and embedded NUL
   bytes are all rejected by `unsafeArchiveName`, and the analysis continues
   with whatever entries remain. `TestUnsafeArchiveEntriesAreIgnored`.
4. An entry declaring more than 2 GiB uncompressed (`DefaultMaxArchiveMember`)
   is also ignored, for the same reason: a zip's declared size is
   attacker-controlled and is a plausible denial-of-service vector on its
   own, before any bytes are even decompressed.
5. Every remaining entry — including the ones that were ignored — is recorded
   in `protocol.ContainerInfo.Members`, by name, size, and the archive's own
   CRC32. This is provenance only; **it is never used for matching.** Two
   zips of the same ROM built by different tools can have completely
   different member metadata and still canonicalize identically.
6. **Exactly one** usable entry after filtering: that entry becomes the
   payload, and canonicalization proceeds against it.
   **Zero** usable entries: the archive is reported as unusable, with the
   reason stated (`"the archive contains no usable entries"`).
   **More than one:** see the next section.

Members are decompressed into memory when the declared size is 64 MiB or
smaller (`DefaultMaxInMemory`) and spilled to a temporary file otherwise, so a
Bridge scanning a library of large DS cartridges doesn't get pushed into swap.
The spill file is always cleaned up, whether analysis succeeds or fails —
`TestOversizedArchiveMembersSpillToDiskAndAreCleanedUp`. Either way, the
declared size is checked against what actually comes out: an entry that lies
about its length is caught rather than silently truncated or over-read.

### Multi-file archives

An archive with more than one usable entry — a `.cue`/`.bin` pair, most
obviously — is **not rejected**. It's recorded: the container info lists every
member, so the holding is visible in a local scan and the data model is fully
exercised. But it produces **no canonical identity**, and an item with no
canonical identity cannot be classified as anything but `unmatched`, which
means it is never advertised, transferred, or counted toward coverage.
`TestMultiMemberArchivesProduceNoCanonicalIdentity`.

This is a direct implementation of Scope of Work §8's non-goal — "Multi-file
and disc-platform transfer in the MVP" — enforced at the point where a
multi-member archive is first seen, rather than relied upon further down the
pipeline.

### Copier headers, byte order, and trimming

These aren't generic archive behavior; they're platform-specific
canonicalization rules, and they're documented against the platform they
apply to rather than here:

- Super Magic Drive copier header removal and de-interleaving:
  `verify/genesis.go`, proven by
  `TestPhase0_GenesisSMDAndBinCanonicalizeIdentically`.
- Trimmed Nintendo DS dump reconstruction: `verify/nds.go`, proven by
  `TestPhase0_TrimmedNintendoDSDumpIsReconstructed`.

No N64 byte-order handling exists, because N64 isn't in the initial platform
set — see [ADR 0004](../adr/0004-initial-platforms.md). If it's added later,
byte-order normalization would need its own adapter and its own fixture, on
the same pattern as the two above.

### What "confirmed" means here concretely

Every claim in this section is exercised in `verify/verify_test.go` against
synthesized fixtures, not real ROM data — see
[ADR 0011](../adr/0011-verification-identity-model.md) for why fixtures are
generated rather than committed. Running `go test ./verify/...` re-confirms
every claim above on demand.

---

## RomM ingestion behavior

### Confirmed, by performing one real upload and reading the server's own logs

A synthetic, clearly-fake ROM (generated by this repository's own
`swarm-fixtures` tool) was uploaded to a live RomM 5.0.0 instance through the
chunked upload API — see [ADR 0003](../adr/0003-supported-romm-versions.md)
for the full sequence — and the server's `/api/logs` endpoint was read
before and after. The relevant lines, in order:

```
INFO  upload   Started chunked upload session <id> for Uncatalogued (USA).gb (1 chunks, 32768 bytes)
INFO  upload   Assembling 1 chunks into /romm/library/gb/roms/Uncatalogued (USA).gb
INFO  upload   Chunked upload complete: /romm/library/gb/roms/Uncatalogued (USA).gb
INFO  watcher  Filesystem event: added /gb/roms/.Uncatalogued (USA).gb.<hash>.assembling
INFO  watcher  Filesystem event: added /gb/roms/Uncatalogued (USA).gb
INFO  watcher  Change detected in gb folder, rescanning in 5 minutes.
```

**API-only upload and filesystem discovery are not two separate paths — they
converge on one.** This corrects an assumption this document previously made
in the opposite direction. The chunked upload API does not appear to hand the
assembled file to some internal indexer directly; it writes the file to the
library directory (staged first under a hidden `.name.hash.assembling` name,
then renamed to its final name — the same stage-then-atomic-publish pattern
`bridge/destination` already uses, independently arrived at) and then RomM's
own filesystem watcher discovers it exactly as it would discover a file placed
there by any other means, including this project's own filesystem publication
mode.

**RomM discovers new content via a filesystem watcher, and debounces a rescan
of the changed folder by five minutes.** The uploaded item did not appear via
`GET /api/roms` until that window elapsed — confirmed by re-querying
immediately (absent) and again after the wait (present, fully populated). This
was a property of the observed instance and version; whether it's configurable
per-instance is not known — see the open question below and in ADR 0003.

**A matched ROM object's shape is now known precisely.** A representative
excerpt of the confirmed fields, from the real response:

```json
{
  "id": 10168,
  "platform_id": 11,
  "platform_slug": "gb",
  "fs_name": "Uncatalogued (USA).gb",
  "fs_size_bytes": 32768,
  "crc_hash": "0b727079",
  "md5_hash": "b3638956a356e7558c3febe5bef7cf27",
  "sha1_hash": "703a66d91e696ffd55fb00faf14d2fb6cd25ffec",
  "ra_hash": "b3638956a356e7558c3febe5bef7cf27",
  "is_identified": false,
  "is_unidentified": true,
  "has_simple_single_file": true,
  "has_multiple_files": false,
  "full_path": "gb/roms/Uncatalogued (USA).gb"
}
```

**RomM's own "identified" concept is orthogonal to this project's
hash-verification.** `is_identified` / `is_unidentified` describe whether RomM
matched the item against a *metadata* provider (IGDB and similar — box art,
description, alternate names). It says nothing about whether the file's bytes
match a preservation-grade reference catalogue, which is what
`protocol.Classification` tracks. The synthetic test file was correctly
`is_unidentified` (no metadata provider recognises it) — consistent with, but
not caused by, its also being `unmatched` in this project's own sense (no
No-Intro entry for a payload that doesn't exist in any real catalogue). A ROM
hack with hand-assigned box art metadata is a case where the two would
disagree: `is_identified` in RomM, still `unmatched` here.

**Hash fields are `crc_hash`, `md5_hash`, `sha1_hash`, and `ra_hash`** — the
first three matching this project's own candidate list; `ra_hash` is
RetroAchievements-specific and was not previously recognised.
`bridge/romm.isHashField` now matches any `*_hash` suffix generically on this
evidence, rather than an enumerated prefix list, so a future RomM-added hash
field is picked up without another code change.

**The chunked upload protocol is fully confirmed** — see ADR 0003 for the
complete sequence — and implemented in `bridge/ingest.RommUploader`, which
replaces `UnprobedUploader` wherever a Bridge is built against a probe that
resolved `roms.upload`.

**The observe side is now real and tested too.** `bridge/ingest.RommLibrary`
implements `Library` against a real server, locating a candidate by the
filename it was uploaded or published under (`Library.Observe` and
`Reconciler.Await` both carry a `filename` parameter for this) and reusing
`bridge/scan.Source` for listing and download rather than a second,
independent implementation of the same calls. It never trusts RomM's
self-reported hash fields: every candidate is downloaded and run back through
the same `verify.Analyzer` the sending side used, so the canonical identity
compared against `Expected` is one this Bridge computed itself.
`TestPhase0_RommLibraryIndependentlyVerifiesRatherThanTrustingRomMsHashes`
proves the independence directly, with RomM's own reported hash deliberately
wrong in the test fixture.

### Still assumed

1. **The five-minute debounce is not necessarily universal.** It was observed
   on one instance running one version. `protocol.DefaultIngestionTimeout` (30
   minutes) has comfortable headroom over it, so no change was made to that
   constant — but if some deployments configure a much longer debounce, that
   headroom should be re-examined rather than assumed adequate everywhere.

2. **`protocol.IngestionPollInterval` (15 seconds) is unconfirmed as a load
   figure.** Scope of Work §7 asks Bridges to avoid unnecessary load on
   participating RomM servers; 15 seconds was chosen to be unhurried rather
   than tight, but nothing has measured what RomM can actually absorb. Given
   the confirmed five-minute debounce, the first several polls after a
   filesystem-mode publish are now known in advance to be no-ops — worth
   considering a longer initial delay before the first poll, though this
   hasn't been changed on the strength of one observed instance.

### What happens when an assumption turns out wrong

The reconciliation loop is built so a wrong assumption produces a visible,
reviewable failure rather than an incorrect success:

- If RomM never reports the expected item within the timeout, the transfer
  lands in `ingestion_timeout` — a review state, never a source.
- If RomM reports *something* but it doesn't match the expected canonical
  identity or platform, the transfer lands in `romm_unmatched` — also a
  review state, also never a source.
- If RomM is simply unreachable during the wait, that's treated as a
  transient condition and retried until the deadline, not as a mismatch
  verdict — `TestUnreachableRommTimesOutRatherThanReportingAMismatch`.

None of these paths can produce a false `source_active`. That protected this
design even where its assumptions were incomplete: the corrected
understanding above (upload and filesystem discovery converging on one path)
would, if wrongly assumed the other way, have shown up as a stuck
`ingestion_timeout` once a real `Library` implementation existed — not a false
`source_active`.
