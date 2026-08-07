# ADR 0014: Refresh rotation recovery

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-06
- **Scope of Work reference:** Phase 0 deliverable "Define the rotating-refresh recovery protocol, including current and previous token hashes, a bounded grace window, transactional persistence, and owner re-enrollment"; Phase 2

## Context

Rotating refresh tokens with reuse detection is the standard defence against a
stolen credential: each use issues a new token and spends the old one, so a
thief and the legitimate holder cannot both keep going — whoever presents the
spent token second reveals the theft.

It assumes a client that does not crash. A Bridge runs on someone's home server.
It gets power-cut, OOM-killed, and restarted mid-upgrade.

Rotation has an unavoidable window. The Host has issued a new token and spent the
old one; the Bridge has not yet received or persisted the new one. A crash there
leaves the Bridge holding a token the Host considers spent — which, under plain
reuse detection, is indistinguishable from theft.

The decision log records why this matters: *"Prevent routine Bridge crashes from
becoming credential-reuse incidents."* A system that revokes a member's Bridge
because their power flickered is one whose security alerts everyone learns to
ignore.

## Decision

The Host keeps **two** hashes per credential family, current and previous, and
allows one bounded recovery.

| Presented | Condition | Outcome |
|---|---|---|
| Current token | — | Normal rotation |
| Previous token | inside the window, same Bridge identity, not yet used | **Recovery.** A fresh credential is issued. |
| Previous token | already used once | Reuse. Family revoked. |
| Previous token | after the window | Reuse. Family revoked. |
| Any token | from a different Bridge identity | Rejected. Family untouched. |
| Unrecognised token | — | Rejected. Family untouched. |

Implemented in `auth/`, with `GraceWindow` at **45 seconds** — the middle of the
30-to-60 second range Phase 2 specifies. The window only has to cover a network
round trip and one fsync.

### Four choices inside that table, and why

**The undelivered token is dropped, not re-sent.** On the recovery path the Host
issues a brand-new credential rather than re-sending the one that was lost. If it
re-sent, it could not distinguish a Bridge that never received the token from one
that did, and the family would carry two live credentials indefinitely.

**A late presentation revokes rather than merely refusing.** A crashed Bridge
retries in seconds. A token surfacing minutes or days later is far more likely to
be a restored backup or a captured credential — and leaving the family alive
would leave that credential live too.

**An unrecognised token does not revoke.** Otherwise anyone who learns a Bridge
identifier could revoke it by sending garbage. Only a token the Host *recognises
as spent* is evidence of anything.

**Recovery is bound to the Bridge identity key.** This is the qualifier that
keeps the window from being a hole. A captured token is not usable from anywhere
else at all, even inside the window, so the recovery path widens the attack
surface only for an attacker who already controls the Bridge — who has no need of
the token.

### Bridge-side persistence

The Host half is only as good as the Bridge half. `auth.FileStore` writes
atomically: temporary file, fsync, rename, fsync the directory. Step four is the
one usually skipped and usually the one that matters — without it the rename can
still be in the journal when power is lost.

`auth.Client.Refresh` persists the new credential *before* returning it. A Bridge
that used a credential it had not stored would, on a crash, hold neither the old
one nor the new one.

### Owner re-enrolment is the only escape

Nothing automatic can resurrect a revoked family. An automatic escape from
revocation is the same thing as not revoking.

## Consequences

- `TestPhase0_RefreshRotationSurvivesACrashAtEveryStep` exercises all five points
  at which a Bridge can lose power during one rotation: before the request, after
  the Host rotated but the reply was lost, and at each of the three steps inside
  the atomic write. After each, the Bridge either carries on or is plainly told
  to re-enrol.
- Recovery and reuse are recorded as **different** event kinds
  (`token.grace_used`, `token.reuse_detected`). An operator seeing a rise in
  recoveries is seeing unstable Bridges — a different problem, with a different
  fix, from seeing reuse.
- `AccessTokenLifetime` is five minutes, which makes it the Host's worst-case
  revocation delay. Phase 2 acceptance requires that "Disabling a Bridge prevents
  new API activity within the access-token lifetime", so the constant is the
  guarantee.
- The Host stores hashes, never tokens. A Host database leak yields no usable
  credentials.

## Open questions

- Should repeated recoveries from one Bridge raise an alert? Several in a day is
  a failing disk or a crash loop, and the owner would want to know.
- Should the access token be bound to the Bridge identity as well, so a leaked
  access token is also unusable elsewhere? Currently only refresh is bound.
