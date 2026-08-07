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

**Not proven. Blocked on live-server access.**

The capability probe that answers this is built and tested
(`bridge/romm`, `TestPhase0_ProbeConfirmsRequiredCapabilities`), and the
capability table names the Phase 0 deliverable behind each requirement. It has
not been run against a real RomM instance: the session that wrote it had outbound
access to the target server denied by network egress policy.

**To close:** run `make probe` against the instance with a scoped standard-user
token, commit the bundle under `fixtures/`, correct any capability whose
candidate paths did not match, and set [ADR 0003](../adr/0003-supported-romm-versions.md)
to Accepted.

The endpoints in `bridge/romm/capability.go` are **assumptions until this is
done.** They are held as a data table with a self-correcting probe precisely
because they are assumptions.

Everything on this path *except the upload transport itself* is now built and
tested: staging, verification while staged, disk-space validation, the ingestion
polling loop with its timeout, and the full receiving flow driving the
destination state machine. The upload is deliberately left as
`ingest.UnprobedUploader`, which fails with an explanation
(`TestPhase0_APIOnlyModeIsBlockedOnAnUnprobedUploadAPI`). RomM's upload endpoint,
chunking scheme, and completion signal are not known to this codebase; writing a
plausible implementation against a guessed protocol would produce code that looks
finished, passes tests against a fake built from the same guess, and fails
against every real server.

Two adjacent Phase 0 deliverables — "Document current multi-file, archive, and
RomM ingestion behavior" — are addressed in
[multi-file-archive-and-ingestion-behavior.md](multi-file-archive-and-ingestion-behavior.md).
Its archive and multi-file sections are confirmed against this codebase's own
tests; its RomM ingestion section is explicitly marked as an assumption this
codebase has built on but never observed, for the same reason as B1 below.

A third deliverable in this group, "Prototype a normalized local inventory
manifest from one RomM server," **is proven**, distinct from the read/write
workflow itself. `bridge/scan` lists a RomM server's inventory, downloads each
item, runs it through the four-identity analysis and reference classification,
and assembles the result into items that `protocol.Manifest.Validate` accepts —
end to end against a purpose-built test server serving real, analyzable
cartridge fixtures, proven in
`TestPhase0_ScanProducesANormalizedManifestFromARomMServer` and
`TestPhase0_ScannedItemsAssembleIntoAPublishableManifest`. It never guesses a
RomM path independently: every request is built from a `romm.Report`'s
already-resolved capability paths, so the paths themselves carry exactly the
same "assumption until probed" caveat as B1, in exactly one place
(`bridge/romm/capability.go`), rather than a second, parallel guess.

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

**Not proven. Requires hardware.**

[ADR 0002](../adr/0002-networking-stack.md) is Open and records the evaluation
criteria, the candidates, and what a proof must demonstrate. **This is the
go/stop gate most likely to stop the project**, and it cannot be closed from a
codebase. No transfer code has been written, deliberately.

### 6. A relayed, interrupted transfer resumes and remains bound to its grant and Bridge identities

**Not proven.** Blocked on the same gate.

### 7. The project has written decisions for the initial technology stack, supported RomM versions, initial platforms, collection profile, plaintext fields, retention, and pilot legal posture

**Partly proven.** All seven have a record; four are still open or proposed.

| Decision | Record | Status |
|---|---|---|
| Technology stack | [0001](../adr/0001-implementation-stack.md) | **Accepted** |
| Supported RomM versions | [0003](../adr/0003-supported-romm-versions.md) | Open — needs a probe run |
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

---

## Blockers

| # | Blocker | Workaround | Deferred to |
|---|---|---|---|
| B1 | **No live RomM instance was reachable.** The build environment's network egress policy denied the CONNECT to the target server (the proxy answered 403), so no request was ever sent. | The probe is built and tested against a fake server. RomM's real paths are held as a correctable data table, and the probe reports every path the real server documents so a mismatch is one line to fix. | Run `make probe` from an environment that can reach the server. |
| B2 | **Direct-plus-relay under CGNAT is unproven.** The Phase 0 go/stop gate. | None. It needs two hosts, one genuinely behind CGNAT. A simulated CGNAT is worth doing first but is not sufficient evidence — the failure modes that matter are carrier-specific. | [ADR 0002](../adr/0002-networking-stack.md). |
| B3 | **No reference catalogues are committed.** No-Intro DATs are not redistributed here. | The importer is proven against synthesised catalogues with real hashes. | Obtain and lock the approved DATs for the five platforms; record versions in [ADR 0004](../adr/0004-initial-platforms.md). |
| B4 | **Legal risk acceptance is not done.** | None, and none is appropriate. | [ADR 0008](../adr/0008-pilot-legal-risk-acceptance.md). |
| B5 | **RomM's upload API shape is unknown**, so API-only import cannot be completed. Downstream of B1. | Everything else on the API-only path is built and tested; the upload is an interface whose only implementation fails with an explanation rather than guessing a protocol. | Closes with B1. |

---

## Go or stop

Scope of Work Phase 0: *"Phase 1 may begin only after API-only import, at least
one viable connectivity path for CGNAT members, and verification for the selected
MVP platforms have been proven."*

| Condition | Status |
|---|---|
| API-only import proven | **No** — B1 |
| A viable CGNAT connectivity path | **No** — B2 |
| Verification for the MVP platforms | **Yes** |

**One of three. Phase 1 must not begin.** The verification model, the protocol
types, the identifier and alias model, the preservation metrics, and the probe
are all in place and tested; the two remaining conditions both need access this
codebase cannot grant itself.
