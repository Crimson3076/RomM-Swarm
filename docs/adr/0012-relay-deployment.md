# ADR 0012: Relay deployment separation

- **Status:** Proposed
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 gate ("Choose whether the relay is operated separately from the Network Host for the pilot"), §5.6, §10

## Context

Scope of Work §5.6 requires the relay to have "Operational isolation from the
Network Host where practical", and §10 lists "Relay becomes a bandwidth or abuse
target" with the response "Separate deployment, quotas, concurrency limits, no
durable storage, and revocable grants".

The relay and the Host have almost opposite operational profiles. The Host is
low-bandwidth, high-value, and holds the index and every credential. The relay is
high-bandwidth, low-value, and should hold nothing at all. Running them together
means the component most likely to be saturated or attacked shares a failure
domain with the component whose availability everything depends on.

## Decision

**Proposed: deploy the relay separately from the Network Host for the pilot.**

Specifically:

- Separate process, separate container, separate host where possible.
- The relay authenticates short-lived transfer grants and nothing else. It never
  receives a RomM credential, never receives a Host database credential, and
  cannot read the catalogue.
- Bounded in-flight buffering only. No durable ROM storage, per §5.6 and §8.
- Byte, time, concurrency, and bandwidth limits, configured independently of the
  Host's limits.
- Relay usage receipts record that a transfer was relayed and how many bytes
  moved. They do not retain exact title names long term, per §5.6.
- The Host remaining available when the relay is saturated is a requirement, not
  a hope: nothing on the Host's critical path may depend on the relay.

## Options considered

### Separate deployment — proposed

Isolates the bandwidth and abuse surface. Lets the relay be scaled, rate-limited,
or replaced without touching the Host. Makes "the relay holds no secrets" a
deployment fact rather than a code discipline. Cost: a second thing to deploy,
monitor, and keep upgraded, for a pilot that may be small.

### Co-deployed for the pilot, separated later

Fewer moving parts while the pilot is small, and the relay is the component most
likely to need iteration. Rejected as a default because "separate later" tends to
mean the Host and relay quietly grow shared state — a shared database handle, a
shared config, a shared in-process cache — and the separation becomes a
refactor rather than a deployment change. If co-deployment is chosen for
convenience, the code boundary must still be enforced as if it were separate.

### No relay for the pilot, direct only

Rejected. §3 makes the encrypted relay fallback a requirement, and Phase 0's
go/stop rule turns on CGNAT members having a viable path. Direct-only excludes
exactly the members the fallback exists for.

## Consequences

- The relay is its own module boundary from the start (`relay/`), with no import
  of Host internals. **Now built this way, not just decided this way:** the
  `relay` package (ADR 0002) imports only `protocol`, never anything
  Host-specific, and its byte/time/concurrency limits and revocation are
  implemented and tested independently of any Host code. This proves the code
  boundary; it does not settle the still-open deployment question below (who
  runs it, on what host, for the pilot).
- Grant verification has to work with only the grant and the Host's public
  verification key. The relay cannot call back into the Host on the hot path
  without becoming a Host dependency.
- Whoever operates the relay is accepting the bandwidth cost. That is a question
  for ADR 0008 as much as a technical one.

## Open questions

- Is the relay operated by the same person as the Host for the pilot? Separate
  deployment does not require separate operators, but the abuse and cost profile
  differs enough that it may want to.
- Does the relay choice fall out of ADR 0002? If the transport selects DERP, the
  relay is a DERP server and much of this is settled by that decision instead.
