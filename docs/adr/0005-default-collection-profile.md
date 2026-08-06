# ADR 0005: Default collection profile

- **Status:** Proposed
- **Phase 0 gate:** yes
- **Date:** 2026-08-06
- **Scope of Work reference:** §4 gate ("default collection profile, such as North America plus World 1G1R"), Phase 4 deliverables and acceptance

## Context

The collection profile is the denominator of every completion percentage. Get it
wrong and the number is not merely inaccurate, it is meaningless — a collection
that is complete in every practical sense can report as a third complete if the
profile counts every regional release of every game as a separate thing to own.

Phase 4 acceptance requires that "A completion percentage names its profile and
reference-set version", which only makes sense if the profile is a first-class,
versioned object rather than an implicit assumption.

## Decision

Ship **"North America plus World 1G1R", version 1**, implemented as
`reference.DefaultProfile()`.

| Rule | Value | Reasoning |
|---|---|---|
| Allowed regions, in priority order | USA, Canada, World | A region-specific North American release is preferred over a World release when both exist, because that is the release a North American collection is normally understood to want. |
| One game per canonical title | yes | Without it the denominator counts regional variants as separate obligations. |
| Revision preference | newest | A later revision is the better-preserved artefact of the same work. |
| Excluded flags | Beta, Proto, Prototype, Demo, Sample, Kiosk, Program, Test Program, Debug, Pirate, Unl | Pre-release and unlicensed material is a different collecting goal. Scope of Work §3 also keeps unmatched hacks, prototypes, demos, and homebrew local in the MVP; excluding their *catalogued* equivalents keeps coverage consistent with that. |
| BIOS entries | excluded | Scope of Work §8 places firmware and BIOS sharing outside the initial release. Counting BIOS entries toward coverage would report progress toward something the project has chosen not to support. |
| Entries with no region tag | included, ranked last | Some catalogues omit the tag; dropping those entries would silently shrink the denominator. Ranking them last means a properly tagged release wins any 1G1R contest. |

A holding that is verified but outside the profile is classified
`verified_excluded` — a genuine dump that does not count toward *this* profile.
It is deliberately not "missing" and not a failure.

## Options considered

### North America plus World 1G1R — chosen

The example the Scope of Work itself names. Produces a denominator that matches
what most members would describe as "a complete collection", and a completion
figure that moves meaningfully when a genuine gap is filled.

### Every region, no 1G1R

Maximally inclusive; the denominator is the whole catalogue. Rejected as a
default because completion figures become permanently low and stop functioning as
a signal — which in turn makes the preservation-risk views (Phase 8) less useful,
since nothing ever looks close to done.

### Region-agnostic 1G1R

One copy of each game, any region. Smaller denominator, arguably a purer
preservation goal. Rejected as the *default* because it makes a Japanese-only
release satisfy the same obligation as a North American one, which surprises
members comparing their library against the Swarm. Worth offering as an
alternative profile.

## Consequences

- Profiles are versioned. Changing any rule above means version 2, and figures
  computed under version 1 remain interpretable.
- `Selection.Size()` is the denominator, computed from the profile applied to a
  reference set — never from what anyone happens to hold.
- Multiple profiles can coexist. Nothing in the model assumes one, and a Swarm
  choosing a different profile does not invalidate another's figures as long as
  each figure names its profile.

## Open questions

- Should the default be per-Swarm rather than global? A European Swarm would
  reasonably want Europe plus World.
- Should `Unl` (unlicensed commercial releases) really be excluded? They are
  legitimately collectible and are not homebrew. Excluded here for consistency
  with the MVP's treatment of unverified content, but this is the rule most
  likely to be wrong.
- Should a member be able to see completion under several profiles at once?
