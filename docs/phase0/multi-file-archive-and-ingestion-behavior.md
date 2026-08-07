# Multi-file, archive, and RomM ingestion behavior

**Status:** Draft — the archive and multi-file sections are confirmed against
this codebase's own tests; the RomM ingestion section is an assumption,
explicitly marked as such, pending a probe run
**Scope of Work reference:** Phase 0 deliverable "Document current multi-file,
archive, and RomM ingestion behavior"

Two different kinds of "current behavior" are documented here, and they carry
different weight. The archive and multi-file sections describe **this
codebase's own code** — `verify/analyze.go` — and every claim names the test
that would fail if the claim stopped being true. The RomM ingestion section
describes **RomM's** behavior, which nothing in this repository has been able
to observe directly; see [ADR 0003](../adr/0003-supported-romm-versions.md)
for why, and treat that section as a set of assumptions the Bridge is built
on, not as confirmed fact.

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

## RomM ingestion behavior (assumed, unconfirmed)

Everything in this section is a **belief the Bridge is built on**, not a
fact this repository has observed. The reason is stated plainly in
[ADR 0003](../adr/0003-supported-romm-versions.md): the environment this
codebase was written in had outbound network access to a real RomM instance
denied by policy, so nothing here has actually watched RomM ingest a file.

### What the Bridge assumes

1. **RomM discovers new content by scanning its library directory**, not by
   being told about individual files. This is the premise behind
   `bridge/ingest.Reconciler`: after a filesystem-mode publish, the Bridge
   polls rather than expecting an immediate signal.
2. **A scan takes an unknown, possibly substantial amount of time**, and may
   not run continuously. `protocol.DefaultIngestionTimeout` is set to 30
   minutes specifically because a premature timeout on a slow scan is worse
   than a stale row in an operator's review queue — see the reasoning in
   `protocol/destination.go`.
3. **API-only uploads are ingested through a different path** than a
   filesystem scan — RomM's own upload endpoint presumably indexes what it's
   given more directly. This assumption is unverified in the other direction
   too: `bridge/ingest.UnprobedUploader` refuses to run at all rather than
   guess at the upload protocol.
4. **RomM exposes, somehow, enough information after ingestion to confirm a
   match**: the platform it filed an item under, and some identity strong
   enough to compare against the canonical digest the Bridge computed.
   `bridge/ingest.Observation` is shaped around this assumption, and its
   `Library` interface is deliberately narrow and swappable so the real
   shape of that information — whatever it turns out to be — can be adapted
   to without changing the reconciliation logic around it.
5. **Polling RomM is not free**, and Scope of Work §7 asks explicitly that
   Bridges avoid unnecessary load on participating RomM servers.
   `protocol.IngestionPollInterval` (15 seconds) is chosen to be unhurried
   rather than tight, on that basis — again, without confirmation of what
   RomM can actually absorb.

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

None of these paths can produce a false `source_active`. That's deliberate:
if the assumptions above turn out to be wrong in some way this design didn't
anticipate, the failure mode is a stuck review queue, not a false claim about
what the Swarm can serve.

### What would confirm or correct this section

Running `swarm-probe` against a real RomM instance, then actually publishing
a file through both destination modes and watching what happens, is the only
way this section moves from "assumed" to "confirmed." Until then, every
constant named above (`DefaultIngestionTimeout`, `IngestionPollInterval`) is
a reasonable-sounding guess, not a measured value, and should be treated as
tunable once real behavior is observed.
