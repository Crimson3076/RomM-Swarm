# Privacy disclosure (draft)

**Status:** Draft, pending review — not yet shown to a real member
**Scope of Work reference:** Phase 0 deliverable "Draft the threat model, minimum plaintext data map, privacy disclosure, and retention schedule"; §7 Privacy

This is the document a prospective member would actually read before accepting
an invitation. [privacy-data-map.md](privacy-data-map.md) is its source of
truth, written for engineers deciding what the schema holds; this is the same
facts, written for someone deciding whether to join. Where the two disagree,
the data map is right and this document is out of date.

It is written in the second person on purpose. A privacy disclosure that talks
about "the user" instead of "you" is easier to leave vague.

---

## What this is

RomM Swarm connects your RomM server to other people's, so you can find games
your library is missing and share the ones you have. A central service — the
**Network Host** — keeps an index of what's available, so your computer never
has to ask every other member's server directly. Content itself never passes
through the Host; it moves directly between your Bridge and another member's.

Everything below describes the Host, because the Host is the one place your
information is centralised at all. Your RomM server, your files, and your
RomM login never leave your own machine.

## What never leaves your Bridge

- Your RomM password.
- The API token your Bridge uses to talk to your RomM server.
- The files themselves, except when you or someone else explicitly requests a
  transfer.
- The filesystem paths on your server. The Host has no reason to know where a
  file sits on your disk, and it never asks.
- Anything you search for. A search is answered and forgotten; it is never
  written to a log, a database row, or anywhere else.
- Any holding you choose not to share with a particular Swarm. If you're in
  two Swarms and share different things with each, the filtering happens on
  your own Bridge before anything is sent — the Host never sees, and can
  never leak, what you didn't send it.

## What the Host does see

- **What you offer, and to which Swarm.** For every file you share, the Host
  learns which game it is, which platform, and whether your copy has been
  verified against a trusted catalogue. This is unavoidable — it's the
  catalogue the whole network is built on.
- **A pseudonym for your Bridge, scoped to each Swarm.** You don't appear to
  other members by your account name; your Bridge gets a different-looking
  identifier in every Swarm you belong to, so someone in two of the same
  Swarms as you can't tell it's the same Bridge just by looking. See
  ["What we can't fully protect you from"](#what-we-cant-fully-protect-you-from)
  below for the honest limit on this.
- **Recent activity, briefly.** If you request or send a file, that request
  is kept for about a month so a failed transfer can be debugged — then it's
  deleted, or reduced to a number that no longer says which title was
  involved.
- **Login attempts and security events, very briefly.** About a week, so
  abuse can be caught, then deleted.
- **Long-term totals that don't name anything.** How many bytes you've
  served, how many requests you've fulfilled, whether a game is well
  preserved across the Swarm — these are kept indefinitely, but as numbers,
  never as a list of what you personally hold or requested.

## How long things are kept

| What | How long |
|---|---|
| Your account and Bridge identity | as long as you're a member |
| What you're currently sharing | as long as you're sharing it |
| A specific request or transfer, by name | about 30 days, then anonymised |
| Login and security events | about 7 days |
| Overall totals and preservation statistics | indefinitely, but never naming a specific title or person |
| Moderation actions taken against an account | indefinitely, so decisions can be reviewed later |

Full detail, including exactly which of these are still a proposal rather
than a settled number, is in
[retention-schedule.md](retention-schedule.md).

## Who can see what

- **Other ordinary members** see the catalogue for Swarms they belong to:
  what's available, roughly how well-preserved it is, and the pseudonym of
  whoever's offering it. They do not see your account name, your email if
  you gave one, your IP address, or what you've searched for or downloaded.
- **Swarm moderators and administrators** see what's needed to run the Swarm
  — membership, reports, and moderation history — not your private activity
  or your unshared library.
- **Whoever operates the Network Host** necessarily sees more than anyone
  else, because the index has to live somewhere. See the next section for
  exactly how much more.

## What we can't fully protect you from

Being straightforward here matters more than sounding reassuring.

- **The Host operator can, in principle, tell that your Bridge in one Swarm
  is the same Bridge as in another Swarm you're in.** The pseudonyms shown to
  other members are designed so *members* can't do this. The Host issues
  those pseudonyms, so it holds what's needed to reverse them. This is a
  limit of the current design, not a secret about it — a future
  privacy-focused mode aims to close this gap, but the version described here
  does not.
- **A member you send something to, or receive something from, can see that
  transfer happened.** That's inherent to direct transfer between two
  Bridges.
- **If content you were legitimately given gets shared further by whoever
  received it, there's no technical control that prevents that.** It's a
  membership and conduct matter, handled through moderation, not through the
  software.
- **Someone who can see both ends of a transfer on the network** — an
  internet provider, for instance — can tell that a transfer of some size
  happened between two addresses, even without knowing what it was.

None of this is a reason not to take the rest seriously. It's a reason not to
overstate what's been built.

## Questions this document doesn't answer yet

- Whether joining requires giving an email address at all, or just accepts an
  invitation code.
- The exact legal terms you'd be agreeing to, and who to contact about a
  piece of content — that's a separate, not-yet-written membership agreement
  ([ADR 0008](../adr/0008-pilot-legal-risk-acceptance.md)).
- The precise retention windows above are proposed defaults, not final
  numbers. See [ADR 0006](../adr/0006-central-plaintext-and-retention.md).

This disclosure will be rewritten once those are settled, and again before
anyone actually sees it at sign-up.
