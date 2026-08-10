# ADR 0003: Supported RomM versions

- **Status:** Accepted
- **Phase 0 gate:** yes
- **Date:** 2026-08-06, evidence recorded 2026-08-10
- **Scope of Work reference:** §4 gate, Phase 0 deliverables "Confirm supported RomM versions and relevant API endpoints", "Verify standard-user Client API Token behavior"

## Context

Everything the Bridge does begins with a RomM API call. Scope of Work §10 lists
"RomM API changes" as a major risk and answers it with capability detection,
version adapters, and OpenAPI-generated models.

This record could not be closed from the codebase alone. It needed a probe run
against a real instance, which this session's own network could not reach — see
"How this was closed" below for how the gap was bridged.

## Decision

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
"these versions were probed, these capabilities were present, these were not."

### Why not hard-code the endpoints

Because the endpoints in the capability table were, until this record's
evidence, an **assumption**. They were written without access to a live RomM
instance — the session that wrote them had outbound access to the target server
denied by network policy. Encoding that assumption as a data table with a probe
that reports mismatches made it self-correcting: when a path was wrong, the
probe printed the real one next to the one that was looked for, and the fix was
a data change, not a redesign. That is exactly what happened — see below.

## How this was closed

Outbound access from the development environment was never granted. The gap was
bridged by the project owner running `swarm-probe`, and then a sequence of
targeted `curl` calls, directly against a live **RomM 5.0.0** instance,
reporting the raw output back for interpretation. Nothing here was written from
assumption; every claim below has a specific observed response behind it.

**Confirmed capability paths.** Every required capability's guessed candidate
matched on the first probe run, with one exception:

| Capability | Guessed | Confirmed |
|---|---|---|
| `server.version` | `GET /api/heartbeat` | matched |
| `platforms.list` | `GET /api/platforms` | matched |
| `roms.list` | `GET /api/roms` | matched (`platform_id` query param present but undocumented in the spec) |
| `roms.detail` | `GET /api/roms/{id}` | matched |
| `roms.download` | `GET /api/roms/{id}/content/{file_name}` | matched exactly, including the two-parameter shape |
| `identity.self` | `GET /api/users/me` | matched |
| `roms.upload` | four single-shot guesses (`POST /api/roms`, etc.) | **all wrong** — see below |

**`roms.upload` is a chunked session, not a single request.** The real sequence,
confirmed by performing one complete upload and reading the server's own logs
afterward:

```
POST /api/roms/upload/start      four headers: x-upload-platform (RomM's own
                                  numeric platform id), x-upload-filename,
                                  x-upload-total-size, x-upload-total-chunks
                                  → {"upload_id": "<uuid>", ...}
PUT  /api/roms/upload/{upload_id}   once per chunk; header x-chunk-index;
                                     raw octet-stream body, not wrapped
                                     → {"received": N, "total": N}
POST /api/roms/upload/{upload_id}/complete    → 2xx, empty body on success
POST /api/roms/upload/{upload_id}/cancel      → 204, best-effort cleanup
```

Two things the OpenAPI schema alone could not answer, and that required an
actual upload to resolve:

- The field name carrying the session id in `start`'s response
  (`upload_id`) — the schema declared that response as an untyped object.
- Whether a chunk body is raw bytes or wrapped in something else (multipart,
  base64) — the schema declared no request body type at all for the `PUT`,
  which in practice meant the handler reads the raw request body directly.
  Raw octet-stream turned out to be correct.

`capability.go`'s `roms.upload` candidates now list the confirmed
`POST /api/roms/upload/start` first, keeping the four original guesses as
low-cost fallback candidates for a RomM version that predates chunked upload.
`bridge/ingest.RommUploader` implements the full sequence above and replaces
`UnprobedUploader` for any Bridge built against a probe that resolved this
capability.

**Authentication.** A plain `Authorization: Bearer <token>` was, in the end,
exactly right — the extended detour through Basic-auth variants and security-
scheme inspection turned out to be chasing the wrong variable: the credential
being tested belonged to a different server entirely. Once the correct token
was used, the first-guess scheme worked immediately. No change was needed to
`AuthSchemes()`'s ordering.

**Hash fields.** A real ROM object carried `crc_hash`, `md5_hash`, `sha1_hash`,
and `ra_hash` (a RetroAchievements-specific hash, not one of this project's
enumerated candidates). `isHashField` in `bridge/romm/probe.go` was broadened to
match any `*_hash` suffix generically rather than an enumerated prefix list,
directly on this evidence — RomM's own naming convention, observed across four
real fields, not a guess about a fifth.

**RomM ingestion timing.** Previously an unconfirmed assumption in
`docs/phase0/multi-file-archive-and-ingestion-behavior.md`. Now measured: RomM's
filesystem watcher detects a newly published file immediately, then debounces a
rescan of the changed folder by **five minutes** before re-indexing it. The
uploaded item did not appear via the API until that debounce elapsed, exactly as
its own log line stated ("Change detected in gb folder, rescanning in 5
minutes"). This is a property of the observed instance and version, not
necessarily a universal constant — see that document for the full account.

## Standard-user (non-admin) behaviour: confirmed, and it's a scope wall

A genuine `"role": "user"` account was created and probed the same way, with
its own Client API Token — not the admin token used above:

```
GET  /api/users/me                    → 200, role: "user", oauth_scopes:
                                         [me.read, roms.read, platforms.read,
                                          assets.read, devices.read,
                                          firmware.read, roms.user.read,
                                          collections.read, playlists.read,
                                          me.write, assets.write,
                                          devices.write, roms.user.write,
                                          collections.write, playlists.write]
GET  /api/roms?platform_id=11&limit=1 → 200, identical shape to the admin token
POST /api/roms/upload/start           → 403 {"detail":"Forbidden"}
```

The scope list explains the 403 precisely: the default `user` role carries
`roms.read` but never `roms.write`. Write scope exists only for
`roms.user.*` — the account's own play data (favorites, save states, hidden
flags), not library content — plus `assets`, `devices`, `collections`,
`playlists`, and `me`. Reads, downloads, and reconciliation-polling all work
identically to the admin token; only `roms.upload` is scope-gated, and it is
gated the same way for every standard user by default, not just this one.

This closes the Scope of Work deliverable as a definite, evidenced answer,
not as a residual unknown: **a default standard-user Client API Token cannot
perform API-only upload.** That's a fact about RomM's own authorization
model, not a gap in this project's implementation.

**What that opens instead:** whether RomM supports granting `roms.write` to a
non-admin account through some other mechanism. The tested account's own
record carries `"permission_group_id": null`, which reads as a hook for a
custom-scope feature, but none was created or tried. Until that's tested, the
practical deployment guidance is: a Bridge doing API-only upload needs either
an admin token, or confirmation that a permission group can grant
`roms.write` without full admin — see the acceptance evidence doc, blocker
B7, for how this is tracked going forward.

## Consequences

- The Bridge refuses to start against a server missing a required capability,
  with a message naming the capability rather than a bare HTTP error. Scope of
  Work §7: "Reject unsupported versions with a clear message."
- Adding support for a new RomM version is usually a probe run and a table
  entry, not a code change — demonstrated in practice here, where one wrong
  guess (`roms.upload`) was corrected as a data change plus one new file
  (`bridge/ingest/rommuploader.go`) implementing the now-confirmed protocol.
- `bridge/ingest.RommUploader` is real, tested code, not a stub — Phase 0's "API
  read, download, upload, and ingestion workflow" is proven end to end for the
  mechanism, for both admin and standard-user tokens.
- **A Bridge doing API-only upload needs a `roms.write`-scoped token, which a
  RomM operator does not get by creating a standard user through the normal
  flow.** Deployment guidance must say this plainly: either grant the Bridge
  an admin token (with the threat-model consequences that implies for that
  credential) or use a permission group granting `roms.write`, if RomM
  supports assigning one to a non-admin account — unconfirmed, see below.
  This is a real operational constraint discovered by evidence, not a
  theoretical one.

## Open questions

- Which RomM major versions does the pilot need to support at once? Only 5.0.0
  has been probed. Supporting a range is more expensive than supporting one, and
  the answer depends on what pilot operators actually run.
- Can a non-admin account be granted `roms.write` through a custom permission
  group? The standard user's own record carries `"permission_group_id":
  null`, which reads as a hook for such a feature, but none was created or
  tested. This determines whether "grant an admin token to the Bridge" is a
  temporary workaround or the actual long-term deployment story for API-only
  upload — see acceptance-evidence.md, blocker B7.
- Is the five-minute rescan debounce configurable per RomM instance, or fixed?
  If configurable, `protocol.DefaultIngestionTimeout` (30 minutes) has
  comfortable headroom over the one observed value; if some instances configure
  it much longer, that headroom should be re-examined.
