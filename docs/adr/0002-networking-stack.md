# ADR 0002: Direct transport and encrypted relay fallback

- **Status:** Accepted for the architecture; the go/stop proof itself remains open — see "What was decided" and "What remains open" below
- **Phase 0 gate:** yes
- **Date:** 2026-08-06, decision recorded 2026-08-10
- **Scope of Work reference:** §4 gate, Phase 0 deliverables "Prove direct transfer on ordinary home NAT and encrypted relay fallback on CGNAT", Phase 0 go/stop rule

## Context

This is the gate most likely to stop the project, and the only one that cannot be
resolved by reading anything. Scope of Work §10 lists "NAT, CGNAT, and firewall
restrictions" as a major risk, and the go/stop rule requires "at least one viable
connectivity path for CGNAT members" before Phase 1 may begin.

The requirement is specific and unusually demanding for a peer-to-peer design:

- Direct Bridge-to-Bridge transfer is preferred (§3).
- An encrypted relay fallback is **required** when direct routing fails (§3).
- No member may be required to configure an inbound port forward (Phase 6
  acceptance).
- Grant, integrity, resume, quota, and receipt behaviour must be **identical**
  across direct and relayed routes (Phase 6 deliverable). This is the constraint
  that rules out bolting a relay on afterwards: the transfer layer must not know
  which route it is on.
- The relay must hold no durable ROM storage beyond bounded in-flight buffering
  (§5.6).

## Decision

**A purpose-built transport over QUIC with hole punching and a custom relay**
— the project owner chose this candidate over tsnet+Headscale+DERP and
libp2p, on the same reasoning recorded under "Candidates to evaluate" below:
smallest trust surface, no overlay-network side effects, and exactly the
per-transfer semantics the Scope of Work describes.

**With one deliberate scoping split**, recorded here so it cannot be mistaken
for the full candidate having been built:

- The **connectivity decision logic** — direct-first with an encrypted-relay
  fallback, a relayed transfer resuming rather than restarting, and a
  mid-transfer route switch that neither loses nor duplicates bytes — is
  built now, in `relay/` and `bridge/transport/`, entirely on the Go standard
  library, and tested against real network connections (loopback TCP).
- The **real QUIC wire protocol with UDP hole punching** is not built yet.
  Go's standard library has no full QUIC implementation — only low-level
  TLS 1.3 hooks meant for others to build one on top of — and hand-rolling an
  encrypted, congestion-controlled, loss-recovering transport protocol from
  scratch is exactly the kind of thing that produces subtle security and
  data-corruption bugs under time pressure. The standard, well-vetted choice
  is `quic-go`, which would be Phase 0's first dependency outside the
  standard library — a direct, deliberate exception to
  [ADR 0001](0001-implementation-stack.md)'s "no dependencies outside the Go
  standard library" rule for Phase 0. That specific exception was proposed to
  the project owner and has not yet been confirmed either way; see "What
  remains open" below. `bridge/transport.DirectDialer` is plain TCP today,
  standing in for whatever the real direct-path transport turns out to be,
  precisely so the decision logic above does not have to be rewritten once
  that choice is made — see `bridge/transport`'s package doc for the exact
  boundary.

This mirrors the split ADR 0001 itself anticipated: "Dependencies arrive with
Phase 1 persistence and Phase 2 Host work." The connectivity *logic* — the
part Phase 0's own acceptance criteria actually ask for evidence of — does
not need to wait for that dependency question to close.

### Evaluation criteria

A candidate must demonstrate all of the following between two home-lab Bridges,
one of them behind CGNAT:

1. A direct path is established when the network permits one.
2. An encrypted relayed path is established when it does not, with no inbound
   port forward on either side.
3. A transfer interrupted mid-flight resumes without restarting from zero, on
   both path types.
4. Route switching mid-transfer does not break the transfer or its grant binding.
5. The relay can be deployed separately from the Host (see ADR 0012) and can be
   given byte, time, concurrency, and bandwidth limits.
6. The relay never needs, and never receives, a RomM credential.

### Candidates to evaluate

- **tsnet with self-hosted Headscale and a self-hosted DERP relay.** The §9
  suggestion. Embeds in-process in Go, gives direct-first with automatic DERP
  fallback, and DERP is already an encrypted relay that stores nothing durably.
  Concerns: it makes every Bridge a node on one overlay network, which is a
  larger trust surface than a per-transfer connection; and Headscale becomes
  infrastructure the project must operate and keep available.
- **A purpose-built transport over QUIC with hole punching and a custom relay.**
  Smaller trust surface, exactly the semantics the Scope of Work describes, no
  overlay-network side effects. Considerably more to build and to get right,
  including the parts that are easy to get subtly wrong.
- **libp2p.** Provides transport, NAT traversal, and circuit relay. Brings a
  large dependency surface and a set of defaults aimed at public networks rather
  than private invitation-only ones.

## How far the evaluation criteria are proven right now

Against loopback TCP, not real hardware — see "What remains open" for exactly
what that does and does not establish.

| # | Criterion | Status |
|---|---|---|
| 1 | Direct path established when possible | **Proven at the decision-logic layer.** `TestPhase0_DirectSucceedsWhenReachable`. |
| 2 | Encrypted relay fallback, no inbound port forward | **Proven at the decision-logic layer.** `TestPhase0_FallsBackToRelayWhenDirectIsUnreachable`; neither side opens an inbound port for the relay path — both dial out to it. Relay-side encryption is real (ordinary server TLS once deployed with a certificate); direct-path encryption is not yet chosen — see below. |
| 3 | Interrupted transfer resumes, both path types | **Proven for the relay path.** `TestPhase0_RelayResumesFromItsOwnRecordedOffsetNotTheClients` (`relay/`), and the relay-only leg of `TestPhase0_RouteSwitchMidTransferDoesNotLoseOrDuplicateBytes`. Resume specifically *on the direct path* is not built — the plain-TCP `DirectDialer` has no resume concept of its own, consistent with encryption-in-transit for direct also being undecided (see below). |
| 4 | Route switch mid-transfer, grant binding preserved | **Proven.** `TestPhase0_RouteSwitchMidTransferDoesNotLoseOrDuplicateBytes`: a transfer starts direct, the direct connection dies mid-flight, and it finishes over the relay under the same `GrantID`/`BridgeID` pair, with the full payload arriving exactly once in order. This test caught a real bug during development — see below. |
| 5 | Relay deployable separately, with limits | **Proven.** `relay/` is its own top-level module with no import of Host internals, per [ADR 0012](0012-relay-deployment.md). `TestPhase0_RelayEnforcesTheConfiguredByteLimit`, `TestPhase0_RelayEnforcesTheConfiguredConcurrencyLimit`, `TestPhase0_RevokeTearsDownAnActiveSessionAndRejectsFutureOnes`. Bandwidth limiting is implemented (`Config.MaxBytesPerSecond`) but not separately test-asserted for throughput, only that the configuration path exists and is wired into the copy loop. |
| 6 | Relay never receives a RomM credential | **Proven structurally, not just by convention.** `TestConnectFrameCarriesNoCredentialField` locks the relay's entire wire protocol (`ConnectFrame`) to exactly `{grant, role, source, sink, resume_offset}` — there is no field to put a credential in, so this can't drift silently as the relay evolves. |

**A real bug the tests caught, worth recording:** the first implementation had
the sender trust the relay's own authoritative byte count unconditionally
when resuming. That is correct when *resuming a relay session that already
existed* (protects against a client understating its own progress to evade
the byte budget — see `relay.ConnectFrame`'s doc comment), but wrong on a
*fresh switch* from direct to relay: a relay that has never carried a given
grant reports 0 bytes moved, regardless of how much the direct path already
delivered. Trusting it unconditionally reset the sender to the beginning,
causing the relay to resend already-delivered bytes while the receiver read
only the tail it still expected — same total length, silently wrong content.
Fixed by resuming from `max(the sender's own tracked progress, the relay's
reported count)` instead of the relay's count alone. `bridge/transport.go`'s
comment on this line documents the reasoning for anyone touching it later.

## Consequences

- `relay/` and `bridge/transport/` exist and are tested — Phase 0's own rule
  ("no transfer code should be written" until this ADR was decided) held
  until the decision above was made, and the code was written after, not
  before.
- Adding the real QUIC/UDP-hole-punching wire protocol later should not
  require rewriting the routing, resume, or grant-binding logic — that is the
  entire reason `bridge/transport.Dialer` is an interface rather than
  `DirectDialer` being hard-wired throughout.
- The Phase 0 go/stop proof still needs real hardware: two hosts, one
  genuinely behind CGNAT. A simulated CGNAT (an unreachable direct address,
  used throughout the tests above) is worth doing first but is not sufficient
  evidence for the go/stop decision on its own, because the failure modes
  that matter are carrier-specific — a real CGNAT's UDP behaviour in
  particular cannot be inferred from a refused TCP loopback connection.

## What remains open

- **Whether to import `quic-go` now, as a scoped exception to ADR 0001, or
  defer the real wire protocol to Phase 1** — proposed to the project owner
  and not yet confirmed either way. Nothing in `relay/` or `bridge/transport/`
  depends on this being resolved in either direction.
- **Which key material authenticates a direct Bridge-to-Bridge connection.**
  No Bridge identity key scheme exists yet in this codebase (no `ed25519` or
  equivalent usage anywhere as of this record). This blocks giving
  `DirectDialer` real encryption and blocks a genuine resume protocol on the
  direct path specifically (criterion 3's direct-path half). The relay path
  does not have this problem: a relay's TLS certificate is an ordinary
  server-TLS deployment fact, not a new identity design.
- **The hardware proof itself**, per the table above — unchanged by
  everything else in this record.

## Open questions

- Does the pilot accept an overlay network as a dependency, or does it require
  transfers to be self-contained? Answered in part by the decision above —
  the chosen candidate avoids an overlay network — but relevant again if the
  QUIC dependency question above is resolved by adopting something with
  wider dependencies than `quic-go` alone.
- Who operates the relay for the pilot, and who pays for its bandwidth?
- What happens to a Bridge that can reach neither a direct peer nor the relay:
  is it a member that can receive but never serve, or is it not a member?
