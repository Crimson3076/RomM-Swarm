# Phase 0 acceptance evidence

**Scope of Work reference:** Phase 0 acceptance criteria and go/stop rule

Scope of Work §13 closes with: *"The first implementation milestone should
produce tests and evidence for the Phase 0 acceptance criteria, not a production
UI."* This document is the ledger for that.

Run the evidence suite:

```
make evidence     # go test -run TestPhase0 -v ./...
make test         # the full suite
```

Every row marked **proven** names a test that fails if the property stops
holding.

---

## Acceptance criteria

### 1. A scoped standard RomM user can complete the API-only read, download, upload, and ingestion workflow without administrator rights

**Confirmed against a real server, and the answer has a real limitation in
it — not a gap in the evidence, a fact about RomM's own default
authorization model.**

A live RomM 5.0.0 instance was probed and then driven through a real,
end-to-end upload — start, chunk, complete — with the server's own logs read
before and after to confirm what actually happened, first with an admin
token and then, separately, with a genuine standard (`"role": "user"`)
account's own Client API Token. Full account in
[ADR 0003](../adr/0003-supported-romm-versions.md). What that closed:

- The capability table's paths are confirmed correct for platforms, listing,
  detail, download, identity, and server version — matched on the first probe
  run. `roms.upload` did not match any of the four guessed candidates; the real
  shape (a chunked session: `POST .../upload/start`, `PUT .../upload/{id}`,
  `POST .../upload/{id}/complete`, `POST .../upload/{id}/cancel`) is now in
  `capability.go`, confirmed rather than guessed.
- `bridge/ingest.RommUploader` replaces `UnprobedUploader` and implements that
  confirmed sequence for real: platform-slug resolution to RomM's numeric id
  (`bridge/romm.FetchPlatforms`, `TestPhase0_PlatformLookupMatchesConfirmedRomMShape`),
  chunked upload with correct reassembly
  (`TestPhase0_RommUploaderMatchesConfirmedRomMBehavior`,
  `TestPhase0_MultiChunkUploadReassemblesInOrder`), and cleanup on failure
  (`TestPhase0_ChunkFailureCancelsTheSession`). Two details the OpenAPI schema
  alone could not answer — the response field naming the upload session
  (`upload_id`), and whether a chunk is a raw byte stream (it is) — were
  resolved by observing one real upload, not guessed a second time.
- Hash field detection was broadened from an enumerated prefix list to a
  generic `*_hash` suffix match, directly on evidence: a real ROM object
  carried `crc_hash`, `md5_hash`, `sha1_hash`, and a fourth,
  RetroAchievements-specific `ra_hash` this project hadn't accounted for.
- The staging, verification, disk-space validation, and destination
  state-machine driving were already built and tested before this round; see
  the properties table below for their specific tests.
- **Standard-user token behaviour is now confirmed, not assumed, and it
  splits along a scope boundary.** A genuine `"role": "user"` account's token
  reads identically to the admin token — `GET /api/roms` returned the same
  shape, 200, fully populated. `POST /api/roms/upload/start` with that same
  token returned `403 {"detail":"Forbidden"}`. The account's own
  `oauth_scopes` explain exactly why: RomM's default `user` role is granted
  `roms.read` but not `roms.write` — write scope exists only for
  `roms.user.*` (the user's own play data: favorites, states, hidden flags —
  not library content) plus `assets`, `devices`, `collections`, `playlists`,
  and `me`. This is RomM's own authorization design, not a probe gap or a
  bug in this codebase: **a default standard-user token cannot perform
  API-only upload at all**, independent of anything RomM Swarm does. Read,
  download, and reconciliation-polling are unaffected — only the write half
  of this criterion is scope-gated.

**Why the write-scope gap doesn't widen the threat model.** A Bridge only
ever writes to its own owner's RomM instance — the architecture has no path
where one member's Bridge writes to another member's server, only reads via
a transfer request. So the practical deployment guidance is simply "an
operator issues their own Bridge an admin-scoped Client API Token for their
own server" — the same person who already administers that server, granting
a credential that never leaves their own Bridge and never reaches past their
own library. Consistent with T1 in the threat model: the credential never
leaves the Bridge.

**What remains open, at low priority:**

- **Whether a non-admin path to `roms.write` exists.** The tested account's
  own record carries `"permission_group_id": null`, which is suggestive of a
  RomM feature for assigning a custom scope set to a non-admin account, but
  this has not been tested — no permission group was created or tried. This
  would only matter for least-privilege hygiene (an operator preferring not
  to grant their Bridge full admin even over their own server); it is not
  required for the architecture to work safely.
- ~~The observe/reconciliation side against real RomM is not yet built.~~
  **Resolved.** `bridge/ingest.RommLibrary` implements `Library` against a
  real server, reusing `bridge/scan.Source` (already proven against the
  confirmed API shape) for listing and download rather than a second,
  independent implementation of the same calls. It locates a candidate by
  the filename the item was uploaded or published under — `Library.Observe`
  and `Reconciler.Await` both gained a `filename` parameter for this,
  threaded from `flow.go`'s already-known `relativeDest` — and never trusts
  RomM's self-reported hash fields: every candidate is downloaded and run
  back through the same `verify.Analyzer` the sending side used, so the
  canonical identity `Reconciler` compares against `Expected` is one this
  Bridge computed itself, not one taken on RomM's word.
  `TestPhase0_RommLibraryIndependentlyVerifiesRatherThanTrustingRomMsHashes`
  proves the independence directly: RomM's own reported hash is deliberately
  wrong in that test, and `RommLibrary` still reports the correct identity
  because it never reads that field.
  `TestPhase0_ReconcilerMatchesAgainstARealRommLibrary` runs `Reconciler` and
  `RommLibrary` together end to end, the same way `flow.go` wires them in
  production. See
  [multi-file-archive-and-ingestion-behavior.md](multi-file-archive-and-ingestion-behavior.md).

Two adjacent Phase 0 deliverables — "Document current multi-file, archive, and
RomM ingestion behavior" — are addressed in
[multi-file-archive-and-ingestion-behavior.md](multi-file-archive-and-ingestion-behavior.md).
Its archive and multi-file sections were already confirmed against this
codebase's own tests; its RomM ingestion section has now moved substantially
from assumed to confirmed, with what remains assumed named explicitly.

A third deliverable in this group, "Prototype a normalized local inventory
manifest from one RomM server," **is proven**, distinct from the read/write
workflow itself. `bridge/scan` lists a RomM server's inventory, downloads each
item, runs it through the four-identity analysis and reference classification,
and assembles the result into items that `protocol.Manifest.Validate` accepts —
originally proven against a purpose-built test server serving real, analyzable
cartridge fixtures (`TestPhase0_ScanProducesANormalizedManifestFromARomMServer`,
`TestPhase0_ScannedItemsAssembleIntoAPublishableManifest`), and its field-name
assumptions (`platform_slug`, `fs_name`, `fs_size_bytes`, `id`) were
subsequently confirmed to match the real server exactly, with no changes
needed.

### 2. Filesystem publication mode works only after explicit operator configuration, and its writable-mount trust boundary is documented

**Proven.**

- The two destination modes are separate, exhaustive, and validated
  (`protocol.DestinationMode`, `TestHandOffStateRejectsUnknownMode`).
- The state machine forbids reaching either destination branch without passing
  through `verified_in_staging` (`TestPhase0_RequiredDestinationPaths`,
  `TestTransitionRejectsUnknownAndIllegalMoves`).
- The preflight refuses unsafe configurations, naming what to change:
  `TestPhase0_PreflightRefusesStagingInsideTheWatchedTree`,
  `TestPreflightRefusesAPublishStagingDirectoryOnAnotherFilesystem`,
  `TestContainmentChecksFollowSymlinks`.
- Publication is atomic and never overwrites:
  `TestPhase0_FilesystemPublicationIsAtomicAndNeverOverwrites`. It uses a hard
  link rather than a rename, so no-overwrite is enforced by the operation itself
  with no check-then-act window.
- A crash leaves nothing partial under a final name, and abandoned staging is
  collected: `TestPhase0_CrashLeavesNoPartialFileUnderTheFinalName`.
- The cross-filesystem path is exercised **against a real device boundary**, not
  a simulation: `TestPhase0_CrossFilesystemStagingIsDetectedAndHandled` locates a
  genuinely different filesystem and skips with an explanation if the machine has
  none.
- Publication cannot escape the scoped writable mount:
  `TestPublishRefusesDestinationsThatEscapeTheLibrary`.
- The trust boundary is documented dedicatedly in
  [filesystem-publication-trust.md](filesystem-publication-trust.md), which
  states what the software guarantees (scoped destination paths, no-overwrite,
  crash safety) against what it cannot (the actual width of the mount you
  configure, the deployment's own privilege separation), and cross-referenced
  from the threat model (T5).

On a platform where the filesystem device cannot be determined, filesystem
publication is refused outright rather than attempted — the same-filesystem
guarantee cannot be made there, and API-only mode remains available.

### 3. A manifest can distinguish exact files, regional or revision variants, and canonical games

**Proven.**

- Exact file: `protocol.FileIDFromCanonicalDigest`, content-derived
  (`TestFileIDIsContentDerivedAndStable`).
- Canonical game: `protocol.GameIDFromReference`, stable across catalogue
  versions (`TestGameIDIgnoresReferenceSetVersion`).
- Regional and revision variants: `reference.ParseName` (`TestParseName`), and
  1G1R selection (`TestOneGamePerCanonicalPicksTheBestRegionAndNewestRevision`).
- Distinct games are not collapsed by similar names: matching is by hash, never
  by name (`TestPhase0_ClassificationStates`).

### 4. Every initial platform has repeatable canonicalization fixtures that accept known-good variants and reject corrupted payloads

**Proven.**

- All five platforms recognised, with the fixture set tied to
  `protocol.InitialPlatforms()` so a platform cannot be added without one:
  `TestPhase0_EveryInitialPlatformIsRecognised`.
- Genesis plain and copier encodings produce one canonical identity:
  `TestPhase0_GenesisSMDAndBinCanonicalizeIdentically`.
- Trimmed DS dumps reconstruct to the full cartridge dump:
  `TestPhase0_TrimmedNintendoDSDumpIsReconstructed`.
- Archived and bare files share a canonical identity:
  `TestPhase0_ArchivedAndBareFilesShareOneCanonicalIdentity`.
- Corrupted payloads are never verified:
  `TestPhase0_CorruptedPayloadsAreNeverVerified`, `TestCorruptedHeadersAreDetected`.
- Truncated dumps are not padded into false validity:
  `TestTruncatedNintendoDSDumpIsNotPaddedIntoValidity`.
- The source is never modified:
  `TestPhase0_CanonicalizationNeverMutatesTheSource`.

Fixtures are generated from a seed rather than committed as binaries, so the
repository carries no ROM data and every fixture is byte-identical on every
machine.

### 5. Two home-lab Bridges can connect directly when possible and fall back to an encrypted relay without inbound port forwarding

**The connectivity decision logic is proven; the hardware proof is not, and
cannot be from this codebase.**

[ADR 0002](../adr/0002-networking-stack.md) is now decided: a purpose-built
transport over QUIC with hole punching and a custom relay. `relay/` and
`bridge/transport/` implement and test the *decision logic* the criteria
actually describe — direct-first, encrypted-relay fallback, resume, route
switching, grant binding — against real network connections (loopback TCP):

- Direct succeeds when reachable: `TestPhase0_DirectSucceedsWhenReachable`.
- Falls back to relay when it isn't (an unreachable direct address, standing
  in for a CGNAT rejection): `TestPhase0_FallsBackToRelayWhenDirectIsUnreachable`.
- Neither side opens an inbound port for the relay path — both dial out to it.
- The relay is a separate module (`relay/`, no Host imports), enforces byte,
  time, and concurrency limits, is revocable, and its entire wire protocol
  has no field for a credential of any kind
  (`TestConnectFrameCarriesNoCredentialField`) — see ADR 0002's evaluation
  table for the full mapping to each criterion.

**What this does not prove:** real UDP hole punching, the actual QUIC wire
protocol (deferred — see ADR 0002), and above all, behavior under a genuine
carrier-grade NAT. A refused TCP loopback connection exercises the same
fallback *code path* a real CGNAT rejection would, but a real CGNAT's UDP
behaviour cannot be inferred from that — the failure modes that matter are
carrier-specific, which is exactly why ADR 0002 has always said this needs
two real hosts, one genuinely behind CGNAT. **This remains the go/stop gate
most likely to stop the project**, now for a narrower and more concrete
reason: not "no code exists," but "the code that exists has not been proven
against the one thing that was always going to require hardware."

### 6. A relayed, interrupted transfer resumes and remains bound to its grant and Bridge identities

**Proven, against real network connections, not yet against real hardware.**

- Resume after an interruption on the relay path:
  `TestPhase0_RelayResumesFromItsOwnRecordedOffsetNotTheClients` — a second
  session for the same grant resumes from the relay's own recorded byte
  count, which is authoritative over whatever the reconnecting client claims
  (protecting against a client understating its own progress to evade the
  byte budget).
- Grant binding across a route switch, specifically:
  `TestPhase0_RouteSwitchMidTransferDoesNotLoseOrDuplicateBytes` — a transfer
  starts direct, the direct connection dies mid-flight, and it finishes over
  the relay under the *same* `GrantID` and `BridgeID` pair, with the full
  payload arriving exactly once, in order. This test caught a real bug during
  development (trusting the relay's authoritative offset unconditionally,
  which is wrong on a fresh route switch since the relay has never seen a
  grant that moved entirely over direct until that point) — see ADR 0002 for
  the fix and the reasoning, since it's a genuine correctness property, not
  just something that happened to make a test pass.

### 7. The project has written decisions for the initial technology stack, supported RomM versions, initial platforms, collection profile, plaintext fields, retention, and pilot legal posture

**Partly proven.** All seven have a record; three are still open or proposed.

| Decision | Record | Status |
|---|---|---|
| Technology stack | [0001](../adr/0001-implementation-stack.md) | **Accepted** |
| Supported RomM versions | [0003](../adr/0003-supported-romm-versions.md) | **Accepted** |
| Initial platforms | [0004](../adr/0004-initial-platforms.md) | Proposed |
| Collection profile | [0005](../adr/0005-default-collection-profile.md) | Proposed |
| Plaintext fields and retention | [0006](../adr/0006-central-plaintext-and-retention.md), [data map](privacy-data-map.md), [schedule](retention-schedule.md) | Proposed |
| Pilot legal posture | [0008](../adr/0008-pilot-legal-risk-acceptance.md) | **Open — needs a person, not a commit** |

The Phase 0 deliverable list additionally names a **privacy disclosure** as
distinct from the data map and retention schedule: the plain-language version
a prospective member would actually read, as opposed to the engineering
record of what's stored. That now exists as
[privacy-disclosure.md](privacy-disclosure.md), explicitly derived from the
data map so the two cannot drift apart silently.

### 8. Known blockers are documented with a workaround or explicitly deferred

**Proven by this document.** See "Blockers" below.

---

## Additional properties proven beyond the stated criteria

| Property | Test | Source |
|---|---|---|
| A file on disk is not a source until `source_active` | `TestPhase0_RequiredDestinationPaths`, `TestSourceActiveIsOnlyReachableFromRommMatched` | Phase 6 |
| Review states never increase coverage or resilience | `TestPhase0_ReviewStatesNeverBecomeSources` | Phase 6 |
| Only `verified_eligible` content is publishable | `TestPhase0_OnlyVerifiedEligibleContentIsPublishable` | §3 |
| One Bridge presents uncorrelatable aliases in two Swarms | `TestPhase0_SwarmAliasesDoNotCorrelateAcrossSwarms` | Phase 2 |
| Long-term metrics are title-free | `TestPhase0_LongTermMetricsAreTitleFree` | §7 |
| Every event kind declares a retention class | `TestPhase0_EveryEventKindDeclaresRetention` | §7 |
| Resilience thresholds: 1 at risk, 2 fragile, 3+ resilient | `TestPhase0_ResilienceThresholds` | Phase 4 |
| Same failure domain is not redundancy | `TestPhase0_SameFailureDomainIsNotRedundancy` | Phase 4 |
| Stale claims do not count | `TestPhase0_StaleClaimsDoNotCountAsResilience` | Phase 4 |
| Offline is unavailable, not missing | `TestPhase0_OfflineIsUnavailableNotMissing` | Phase 4 |
| Transfer acceptance requires SHA-256, not catalogue hashes | `TestPhase0_TransferAcceptanceRequiresStrongAgreement` | §3 |
| The probe never records the credential | `TestPhase0_ProbeOutputNeverContainsTheCredential` | §7 |
| Refresh rotation survives a crash at every step | `TestPhase0_RefreshRotationSurvivesACrashAtEveryStep` | Phase 0, Phase 2 |
| Recovery is bound to the Bridge identity key | `TestPhase0_RecoveryIsBoundToTheBridgeIdentity` | Phase 2 |
| Reuse outside the grace path revokes the family | `TestPhase0_ReuseOutsideTheGracePathRevokesTheFamily` | Phase 2 |
| Owner re-enrolment is the only escape from revocation | `TestPhase0_OwnerReEnrolmentIsTheOnlyEscapeFromRevocation` | Phase 2 |
| Filesystem publication is atomic and never overwrites | `TestPhase0_FilesystemPublicationIsAtomicAndNeverOverwrites` | Phase 6 |
| A crash leaves nothing partial under a final name | `TestPhase0_CrashLeavesNoPartialFileUnderTheFinalName` | Phase 6 |
| Cross-filesystem staging is detected and handled | `TestPhase0_CrossFilesystemStagingIsDetectedAndHandled` | Phase 6 |
| Unsafe staging configurations are refused | `TestPhase0_PreflightRefusesStagingInsideTheWatchedTree` | Phase 1 |
| Verification happens while staged, on SHA-256 | `TestPhase0_VerificationHappensWhileStaged` | Phase 6 |
| One Bridge publishes different manifests without leaking | `TestPhase0_OneBridgePublishesDifferentManifestsWithoutLeaking` | Phase 1, Phase 3 |
| Unverified content is never published under any policy | `TestPhase0_UnverifiedContentIsNeverPublished` | §3 |
| Repeated scans produce no false additions or deletions | `TestPhase0_RepeatedScansProduceNoChange` | Phase 1 |
| The full receiving flow reaches source_active only after RomM matches | `TestPhase0_FilesystemFlowReachesSourceActiveOnlyAfterRommMatches` | Phase 6 |
| Ingestion timeout is a visible review state, not a source | `TestPhase0_IngestionTimeoutIsAVisibleReviewStateNotASource` | Phase 6 |
| A mismatched ingestion never activates | `TestPhase0_MismatchedIngestionIsAReviewState` | Phase 6 |
| A corrupted payload never reaches the library | `TestPhase0_CorruptedPayloadIsRejectedBeforeItReachesTheLibrary` | Phase 6 |
| API-only mode's remaining blocker fails loudly rather than guessing | `TestPhase0_APIOnlyModeIsBlockedOnAnUnprobedUploadAPI` | Phase 0 |
| A RomM listing scans into a manifest-ready set of items | `TestPhase0_ScanProducesANormalizedManifestFromARomMServer` | Phase 0 |
| Scanned items assemble into a validated manifest | `TestPhase0_ScannedItemsAssembleIntoAPublishableManifest` | Phase 0, Phase 3 |
| A chunked upload matches confirmed real RomM behaviour end to end | `TestPhase0_RommUploaderMatchesConfirmedRomMBehavior` | Phase 0, Phase 6 |
| Multi-chunk uploads reassemble in order across a non-round boundary | `TestPhase0_MultiChunkUploadReassemblesInOrder` | Phase 6 |
| A failed chunk cancels the server-side session rather than abandoning it | `TestPhase0_ChunkFailureCancelsTheSession` | §7 |
| A platform lookup matches the confirmed real RomM object shape | `TestPhase0_PlatformLookupMatchesConfirmedRomMShape` | Phase 0 |
| Duplicate platform slugs resolve deterministically | `TestPhase0_DuplicateSlugsResolveToTheFirstEntry` | Phase 0 |
| A malformed platform row is skipped, not fatal to the whole fetch | `TestPhase0_MalformedPlatformRowsAreSkipped` | Phase 0 |
| The observe side independently re-verifies rather than trusting RomM's hash | `TestPhase0_RommLibraryIndependentlyVerifiesRatherThanTrustingRomMsHashes` | Phase 0, Phase 6 |
| A candidate is located by filename across multiple listing pages | `TestPhase0_RommLibraryLocatesByFilenameAcrossPages` | Phase 0 |
| Reconciler and the real observe-side Library work together end to end | `TestPhase0_ReconcilerMatchesAgainstARealRommLibrary` | Phase 0, Phase 6 |
| A direct path is used when the peer is reachable | `TestPhase0_DirectSucceedsWhenReachable` | Phase 0 |
| An unreachable direct peer falls back to the encrypted relay | `TestPhase0_FallsBackToRelayWhenDirectIsUnreachable` | Phase 0 |
| A route switch mid-transfer loses or duplicates no bytes | `TestPhase0_RouteSwitchMidTransferDoesNotLoseOrDuplicateBytes` | Phase 0 |
| A relayed session resumes from the relay's own recorded offset | `TestPhase0_RelayResumesFromItsOwnRecordedOffsetNotTheClients` | Phase 0 |
| The relay enforces its configured byte and concurrency limits | `TestPhase0_RelayEnforcesTheConfiguredByteLimit`, `TestPhase0_RelayEnforcesTheConfiguredConcurrencyLimit` | Phase 0 |
| Revoking a grant tears down an already-active relayed session | `TestPhase0_RevokeTearsDownAnActiveSessionAndRejectsFutureOnes` | Phase 0 |
| The relay's wire protocol has no field for any credential | `TestConnectFrameCarriesNoCredentialField` | §7 |
| An empty RomM library is never mistaken for a missing hash field | `TestPhase0_EmptyLibraryIsNotMistakenForAMissingHashField`, `TestFirstObjectDoesNotMistakeAnEmptyEnvelopeForAnObject` | Phase 0 |

---

## Blockers

| # | Blocker | Workaround | Deferred to |
|---|---|---|---|
| ~~B1~~ | ~~No live RomM instance was reachable from this codebase's own environment.~~ **Resolved by the project owner running the probe and a sequence of manual requests directly, reporting the results back for interpretation.** The environment's own egress policy is unchanged and still denies the connection; the workaround was procedural, not technical. | — | Closed. See [ADR 0003](../adr/0003-supported-romm-versions.md). |
| B2 | **Direct-plus-relay under real CGNAT is unproven.** Narrowed from "no candidate chosen and no code written" — ADR 0002 is now decided (custom QUIC with hole punching) and the connectivity decision logic (`relay/`, `bridge/transport/`) is built and tested against real network connections. The Phase 0 go/stop gate itself is unchanged: it was always specifically the hardware proof, not the code. | None. It needs two hosts, one genuinely behind CGNAT. A simulated CGNAT (an unreachable direct address, already exercised in tests) is worth doing first but is not sufficient evidence — the failure modes that matter are carrier-specific, especially for UDP. | [ADR 0002](../adr/0002-networking-stack.md). |
| B3 | **No reference catalogues are committed.** No-Intro DATs are not redistributed here. | The importer is proven against synthesised catalogues with real hashes. | Obtain and lock the approved DATs for the five platforms; record versions in [ADR 0004](../adr/0004-initial-platforms.md). |
| B4 | **Legal risk acceptance is not done.** | None, and none is appropriate. | [ADR 0008](../adr/0008-pilot-legal-risk-acceptance.md). |
| ~~B5~~ | ~~Standard-user (non-admin) token behaviour is unverified.~~ **Resolved: verified and it's a hard scope boundary, not a gap.** A real `"role": "user"` token reads exactly like admin but gets `403 Forbidden` on `roms.upload` — RomM's default user role has `roms.read` but not `roms.write`. Does not widen the threat model: a Bridge only ever writes to its own owner's instance, so the practical guidance is that operator issues their own Bridge an admin token for their own server — a credential they already effectively hold as that server's admin. | An operator's Bridge uses an admin-scoped Client API Token for their own RomM instance. | Closed as a finding. See ADR 0003. Low-priority follow-up on least-privilege hygiene — B7. |
| B7 (low priority) | **Whether a non-admin account can be granted `roms.write` via a custom permission group is unknown.** The tested user's own record carries `"permission_group_id": null`, suggesting the feature exists, but none was created or tested. Not a blocker — see B5. | Deploy with an admin token for now; this is a nicer default, not a requirement. | Test creating a permission group with `roms.write` against a real instance, if and when convenient; document the result in ADR 0003. |
| ~~B6~~ | ~~A production `Library` implementation for ingestion reconciliation against real RomM is not built.~~ **Resolved.** `bridge/ingest.RommLibrary` implements `Library` for real, reusing `bridge/scan.Source`'s already-proven listing and download, and independently re-verifies every candidate by downloading and recomputing its canonical identity rather than trusting RomM's self-reported hash — `TestPhase0_RommLibraryIndependentlyVerifiesRatherThanTrustingRomMsHashes` proves the independence with a deliberately wrong reported hash. | — | Closed. See `bridge/ingest/rommlibrary.go` and multi-file-archive-and-ingestion-behavior.md. |

---

## Go or stop

Scope of Work Phase 0: *"Phase 1 may begin only after API-only import, at least
one viable connectivity path for CGNAT members, and verification for the selected
MVP platforms have been proven."*

| Condition | Status |
|---|---|
| API-only import proven | **Yes.** Every capability path and the full chunked-upload protocol are confirmed against a real server and implemented as tested code, for both admin and standard-user tokens. Standard-user upload is confirmed scope-blocked by RomM itself (`roms.write` isn't in the default `user` role); that resolves to "an operator uses an admin token for their own instance," which doesn't widen the threat model — see B5. The observe side (`bridge/ingest.RommLibrary`) is now real and tested too — see B6, closed. |
| A viable CGNAT connectivity path | **No, still — but the reason narrowed.** B2. The routing, resume, and route-switch decision logic is now built and tested (`relay/`, `bridge/transport/`); what's missing is specifically the hardware proof against a real carrier-grade NAT, which this codebase cannot supply. |
| Verification for the MVP platforms | **Yes** |

**Still not all three. Phase 1 must not begin.** API-only import is fully
proven — both the write side (`RommUploader`) and the observe side
(`RommLibrary`) are real, tested code against RomM's confirmed protocol, for
both admin and standard-user tokens. The verification model, the protocol
types, the identifier and alias model, the preservation metrics, the probe,
and now the connectivity decision logic (ADR 0002, `relay/`,
`bridge/transport/`) are all in place and tested. The remaining gate is
entirely B2, and entirely the hardware half of it: everything code could
prove about direct-plus-relay connectivity without two real hosts, one behind
genuine CGNAT, has now been proven. That specific proof needs hardware this
codebase cannot grant itself.
