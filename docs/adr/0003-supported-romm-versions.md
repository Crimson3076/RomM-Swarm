# ADR 0003: Supported RomM versions

- **Status:** Open
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 gate, Phase 0 deliverables "Confirm supported RomM versions and relevant API endpoints", "Verify standard-user Client API Token behavior"

## Context

Everything the Bridge does begins with a RomM API call. Scope of Work §10 lists
"RomM API changes" as a major risk and answers it with capability detection,
version adapters, and OpenAPI-generated models.

This record cannot be closed from the codebase. It needs a probe run against real
instances of the versions under consideration.

## Decision

**Not yet made.** What has been decided is the *method*, which is the part that
does not need a live server:

The supported-version question is answered by **capability detection against the
server's own OpenAPI document**, not by a version allowlist. `bridge/romm`
implements this:

- `capability.go` holds a declarative table of the capabilities RomM Swarm needs,
  each naming the Phase 0 deliverable that depends on it.
- The probe reads `/openapi.json`, matches the table against the documented
  operations, and reports what matched, what did not, and every path the server
  actually documents.
- A missing **required** capability is reported as a Phase 0 blocker, and
  `swarm-probe` exits non-zero, so it can gate a script.

A version allowlist is then derived from probe evidence rather than guessed at:
"these versions were probed, these capabilities were present, these were not".

### Why not hard-code the endpoints

Because the endpoints in the capability table are currently an **assumption**.
They were written without access to a live RomM instance — the session that wrote
them had outbound access to the target server denied by network policy. Encoding
that assumption as a data table with a probe that reports mismatches makes it
self-correcting: when a path is wrong, the probe prints the real one next to the
one that was looked for, and the fix is one line in the table.

Hard-coding the same assumption into client code would have produced software
that fails at runtime with a 404 and no explanation.

## What is still needed

1. Run `swarm-probe` against each RomM version under consideration:

   ```
   ROMM_URL=https://romm.example ROMM_TOKEN=<scoped standard-user token> \
     make probe
   ```

2. Commit the resulting bundles under `docs/phase0/fixtures/`.
3. Correct any capability whose candidate paths did not match, using the
   `path_inventory` in the bundle.
4. Confirm, with a **standard-user** token and no administrator rights:
   - inventory listing with pagination
   - per-ROM metadata including hashes, size, and platform identifier
   - content download
   - `roms.write` upload
5. Record the outcome here and set this record to Accepted.

## Consequences

- The Bridge refuses to start against a server missing a required capability,
  with a message naming the capability rather than a bare HTTP error. Scope of
  Work §7: "Reject unsupported versions with a clear message."
- Adding support for a new RomM version is usually a probe run and a table entry,
  not a code change.

## Open questions

- Which RomM major versions does the pilot need to support at once? Supporting
  one is far cheaper than supporting a range, and the answer depends on what the
  pilot operators are actually running.
- Does the Client API Token carry inspectable scopes? If it does, the Bridge can
  self-test its permissions at setup time, which Phase 1 lists as a deliverable
  ("Scope and permission self-test"). If it does not, the self-test has to be
  behavioural, which is more intrusive against a live library.
