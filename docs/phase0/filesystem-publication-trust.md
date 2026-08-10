# Filesystem publication: trust and host impact

**Status:** Draft, pending review
**Scope of Work reference:** Phase 0 deliverable "Document the additional trust and host impact of filesystem publication mode"; §3, Phase 1 acceptance, Phase 6

This is written for the person deciding whether to turn filesystem publication
on for their own Bridge. API-only mode is the default this document assumes
you're comparing against; the question here is specifically *what changes*
when you enable the alternative.

---

## The one-sentence version

API-only mode gives the Bridge process no access to your RomM library at all;
filesystem publication gives it a writable path into part of it. Everything
below is about what that second grant of access costs you, and what the code
does and does not do to limit the cost.

## What each mode actually requires of your host

| | API-only mode | Filesystem publication |
|---|---|---|
| RomM library filesystem access | none | read-write, to a scoped subtree |
| What the Bridge process can reach if compromised | the network, your RomM Client API Token, its own staging directory | the above, **plus** anywhere under the writable mount |
| What a bug in the Bridge can do to your library | nothing directly — RomM's own upload API is the only path in, and RomM enforces its own rules on what that accepts | write files under the mount; a sufficiently bad bug could place, but never silently overwrite, files there |
| What your RomM server has to trust | a Client API Token, scoped like any other client | the same token, **plus** the operating system's own file permissions on the mount |

The Client API Token is present in both modes and is not the delta this
document is about — see [threat-model.md](threat-model.md), T1, for that.
This document is only about the *filesystem* grant that filesystem publication
adds on top.

## Why the delta matters even though the transfer itself is safe

The transfer protocol is the same story in both modes: stage outside the
watched tree, verify while staged, only then hand off. That part is proven —
see [acceptance-evidence.md](acceptance-evidence.md), criterion 2, and the
tests under `bridge/destination`.

What changes is not *how a publish happens*, but *what else becomes possible*
once the Bridge process holds a writable path into your library at all. Four
concrete differences:

1. **A compromised Bridge process becomes a compromised subtree of your
   library**, not just a compromised network client. If someone found a way
   to run arbitrary code inside the Bridge, API-only mode limits what they
   reach on your machine to the Bridge's own staging directory and whatever
   your RomM API token permits through RomM's own API surface. Filesystem
   publication additionally gives them direct filesystem access to
   everything under the mount, without RomM's API in the loop at all.

2. **A bug in the Bridge is now a filesystem bug, not just a network bug.**
   `TestPhase0_FilesystemPublicationIsAtomicAndNeverOverwrites` and
   `TestPublishRefusesDestinationsThatEscapeTheLibrary` exist precisely
   because "write a file to a path we computed" is a much larger space of
   things to get wrong than "make an HTTP request". The tests cover the
   failure modes this codebase has thought of; they cannot cover the ones it
   hasn't.

3. **The operating system's permission model now matters.** In API-only
   mode, file permissions on your library are irrelevant to the Bridge — it
   never touches the filesystem there. In filesystem publication mode, the
   Bridge's write access is exactly as broad as the mount you gave it. The
   software enforces a **narrow scope** (see below); the **actual scope** is
   whatever directory you point it at, and that's a deployment decision the
   software cannot make for you.

4. **RomM's own scanning is now reading files a second, independent writer
   put there — though this matters less than it first appears to.** Evidence
   from a live RomM 5.0.0 instance (see
   [ADR 0003](../adr/0003-supported-romm-versions.md)) showed that even the
   upload API doesn't hand a file to RomM's indexer directly: it writes the
   assembled file into the library directory and then RomM's own filesystem
   watcher discovers it, debounced by five minutes, exactly as it would
   discover a file placed there by any other means. API-only and filesystem
   publication converge on the same discovery path; the difference is only
   *who* wrote the file, not how RomM notices it. This is why the
   ingestion-reconciliation wait exists for both modes — see
   [acceptance-evidence.md](acceptance-evidence.md) criterion 1 and
   `bridge/ingest` — and why a file on disk is never treated as a Swarm
   source before RomM confirms it found and matched it.

## What the code guarantees, and what it can't

**Guaranteed by the software, and tested:**

- The Bridge refuses to start in filesystem publication mode against an
  unsafe configuration: staging inside the watched tree, a publication
  staging directory on a different filesystem from the library, or a symlink
  that disguises either. See `bridge/destination/publish.go`, `Preflight`,
  and `TestPhase0_PreflightRefusesStagingInsideTheWatchedTree`.
- A destination path is confined to the configured library root. A transfer
  cannot be made to write outside it, however it's named.
  `TestPublishRefusesDestinationsThatEscapeTheLibrary`.
- Publication never overwrites an existing file. It uses a hard link, which
  fails outright if the destination already exists, rather than a rename,
  which would silently replace it. `TestPhase0_FilesystemPublicationIsAtomicAndNeverOverwrites`.
- A crash at any point leaves either nothing at the final path or the
  complete verified file — never a partial one under its final name.
  `TestPhase0_CrashLeavesNoPartialFileUnderTheFinalName`.
- A cross-filesystem configuration is detected before anything is copied,
  and is never described as an atomic move.
  `TestPhase0_CrossFilesystemStagingIsDetectedAndHandled`.

**Not, and cannot be, guaranteed by the software:**

- **That the mount you configure is actually narrow.** If you point
  `LibraryRoot` at your entire disk instead of your RomM library folder, the
  preflight has no way to know that's wrong — from the software's point of
  view, a broad mount and a narrow one look identical. Scope the mount to
  exactly the RomM library directory and nothing else.
- **That the account the Bridge runs as has no other access.** If the
  Bridge's container or user account can also reach your RomM database,
  other services, or unrelated data, filesystem publication doesn't cause
  that exposure, but it doesn't reduce it either. Least-privilege deployment
  is the operator's responsibility; the software can only be conservative
  with the access it's actually given.
- **That RomM's own scan behavior is fully characterized across every
  deployment.** The core mechanism is now confirmed against a live RomM
  5.0.0 instance — filesystem watcher, five-minute rescan debounce, same
  discovery path for both modes — see
  [ADR 0003](../adr/0003-supported-romm-versions.md) and
  [multi-file-archive-and-ingestion-behavior.md](multi-file-archive-and-ingestion-behavior.md).
  What's still unconfirmed is whether that debounce is a fixed constant or
  configurable per instance; a much longer debounce on some deployment would
  affect filesystem publication and API-only ingestion equally, since both
  now wait on the same watcher.

## Recommendation

Enable filesystem publication only when you specifically need it — typically,
when your RomM instance's own upload path can't be used for some reason — and
mount **only** the RomM library directory, read-write, with the Bridge running
as a user or container that has no other access to your system. Treat the
mount itself as the trust boundary: the software enforces safety on the
filesystem side of that boundary; you're responsible for how wide the boundary
is.

This is why filesystem publication is a separate, explicit opt-in rather than
a default — Scope of Work §3, and Phase 1 acceptance: "Filesystem publication
mode refuses to start with an unsafe, overly broad, or incompatible staging
configuration." The refusal covers what the software can check. The scope of
the mount is what it can't.
