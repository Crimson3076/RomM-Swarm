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

The RomM 5.0 endpoints in `bridge/romm/capability.go` are backed by upstream
source inspection, including the `roms.read` download and `roms.write` chunked
upload routes. Their behavior with a real scoped user, reverse proxy, watcher,
and library remains unproven until the live exercise is recorded.

### 2. Filesystem publication mode works only after explicit operator configuration, and its writable-mount trust boundary is documented

**Partly proven.**

- The two destination modes are separate, exhaustive, and validated
  (`protocol.DestinationMode`, `TestHandOffStateRejectsUnknownMode`).
- The state machine forbids reaching either destination branch without passing
  through `verified_in_staging` (`TestPhase0_RequiredDestinationPaths`,
  `TestTransitionRejectsUnknownAndIllegalMoves`).
- The trust boundary is documented in the threat model (T5) and
  [ADR 0007](../adr/0007-browser-delivery.md) records the related delivery
  decision.

**Outstanding:** the filesystem preflight itself — same-filesystem detection,
atomic no-overwrite rename, `fsync` of the destination directory, crash recovery.
That is Phase 1 and Phase 6 work and requires a disposable library to test
against.

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

---

## Blockers

| # | Blocker | Workaround | Deferred to |
|---|---|---|---|
| B1 | **No live RomM instance was reachable.** The build environment's network egress policy denied the CONNECT to the target server (the proxy answered 403), so no request was ever sent. | The probe is built and tested against a RomM 5.0 source-backed fake. The real paths are held as a correctable data table, and the probe reports every path the deployed server documents. | Run `make probe` and the opt-in synthetic upload exercise from an environment that can reach the server. |
| B2 | **Direct-plus-relay under CGNAT is unproven.** The Phase 0 go/stop gate. | None. It needs two hosts, one genuinely behind CGNAT. A simulated CGNAT is worth doing first but is not sufficient evidence — the failure modes that matter are carrier-specific. | [ADR 0002](../adr/0002-networking-stack.md). |
| B3 | **No reference catalogues are committed.** No-Intro DATs are not redistributed here. | The importer is proven against synthesised catalogues with real hashes. | Obtain and lock the approved DATs for the five platforms; record versions in [ADR 0004](../adr/0004-initial-platforms.md). |
| B4 | **Legal risk acceptance is not done.** | None, and none is appropriate. | [ADR 0008](../adr/0008-pilot-legal-risk-acceptance.md). |
| B5 | **Filesystem publication preflight is unimplemented.** | The state machine forbids the unsafe transitions; the preflight itself needs a disposable library. | Phase 1 and Phase 6. |

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
