# ADR 0002: Direct transport and encrypted relay fallback

- **Status:** Open
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
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

**Not yet made.** This record exists so the gate is tracked, and so the
evaluation has criteria written before a candidate is chosen.

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

## Consequences

Until this is decided:

- No transfer code should be written. `protocol/destination.go` deliberately
  defines the *receiving* state machine, which is transport-independent, and
  stops there.
- The Phase 0 proof needs real hardware: two hosts, one genuinely behind CGNAT.
  A simulated CGNAT is worth doing first but is not sufficient evidence for the
  go/stop decision, because the failure modes that matter are carrier-specific.

## Open questions

- Does the pilot accept an overlay network as a dependency, or does it require
  transfers to be self-contained?
- Who operates the relay for the pilot, and who pays for its bandwidth?
- What happens to a Bridge that can reach neither a direct peer nor the relay:
  is it a member that can receive but never serve, or is it not a member?
