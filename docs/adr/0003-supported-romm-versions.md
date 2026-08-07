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

### First compatibility target

RomM 5.0 is the first source-backed target. The candidate operations are tied to
[upstream commit `960e18d`](https://github.com/rommapp/romm/tree/960e18df77da261d082f49205d809549a1c994a1):

- [Client API Tokens](https://github.com/rommapp/romm/blob/960e18df77da261d082f49205d809549a1c994a1/backend/handler/auth/hybrid_auth.py)
  are bearer credentials whose effective scopes are the intersection of the
  token and owning user's scopes.
- [`GET /api/roms/{id}/files/content/{file_name}`](https://github.com/rommapp/romm/blob/960e18df77da261d082f49205d809549a1c994a1/backend/endpoints/roms/files.py)
  requires `roms.read`.
- Chunked upload is `POST /api/roms/upload/start`, `PUT
  /api/roms/upload/{upload_id}`, and `POST
  /api/roms/upload/{upload_id}/complete`; every step requires `roms.write`, not
  administrator status. The routes are defined in
  [`upload.py`](https://github.com/rommapp/romm/blob/960e18df77da261d082f49205d809549a1c994a1/backend/endpoints/roms/upload.py).
- Interrupted sessions can be cleaned up with `POST
  /api/roms/upload/{upload_id}/cancel`.

Source inspection proves the intended authorization and route shape. It does
not prove a real reverse-proxied deployment, token configuration, ingestion
latency, or watcher behavior, so this ADR remains Open until a live probe and
synthetic upload exercise are recorded.

The table remains capability-based instead of hard-coding a version allowlist.
When a path changes, the probe prints the server's documented operation next to
what was expected, and the adapter can be corrected in one place.

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
- Which response should the Bridge treat as the authoritative effective-scope
  view: `/api/users/me`, `/api/permissions/me`, or both? The upstream model is
  inspectable, but the live response schema still needs to be captured.
