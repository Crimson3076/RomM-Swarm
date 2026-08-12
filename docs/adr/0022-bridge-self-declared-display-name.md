# ADR 0022: Bridge self-declared display name

- **Status:** Accepted
- **Phase 0 gate:** no
- **Date:** 2026-08-12
- **Scope of Work reference:** direct follow-on to ADR 0019 (inventory
  publishing) and ADR 0018 (Host web UI, which introduced
  `bridge_swarm_memberships.display_name`).

## Context

ADR 0018 gave each Bridge a per-Swarm `display_name` the Host owner can set,
explicitly deciding it would be **"Host-assigned, not Bridge-self-declared"**
— reasonable when a Bridge's identity was just a `BridgeID`/alias and the
Host owner was the only party who'd ever look at the list. With inventory
publishing (ADR 0019/0020) now running automatically, that list is no longer
occasional reading: an owner managing several Bridges sees it regularly, and
a raw `BridgeID` is not something anyone can eyeball and recognize.

The user asked for this directly: a Bridge owner should be able to set their
own Bridge's name (e.g. "Dallas's RomM Bridge") and have it show up on the
Host, without requiring the Host owner to rename every Bridge by hand.

This reopens ADR 0018's decision rather than replacing it outright — the
Host owner's own judgment (they may want to relabel a Bridge to fit their
own naming scheme, or to disambiguate two Bridges publishing similar names)
still needs to win when they've actually exercised it.

## Resolved sub-decisions

Both resolved via `AskUserQuestion`, answers below.

1. **Precedence: the Host owner's manual rename, once made, sticks until
   explicitly cleared — it is never silently overwritten by a
   Bridge-published name.** A Bridge's self-declared name only ever fills
   an *unset* slot. This preserves ADR 0018's original guarantee (the Host
   owner has final say over what they see) while letting a Bridge's own
   choice be the default when nobody has overridden it. Clearing a
   Host-set name (setting it back to empty via the same Host UI control)
   reopens that slot to the next Bridge-published name.
2. **Where a Bridge sets its own name: a field on the Bridge's own Settings
   page** (`bridge/adminui`), sent with every inventory publish — not a
   separate dedicated route, not something asked for only at enrollment
   time. This matches how every other piece of Bridge-local configuration
   already works (`bridge/bridgeconfig.Config`, editable any time through
   Settings) and means renaming later is as easy as changing a text field,
   no re-enrollment required.

## Concrete design

### Storage: one new boolean, not a new table

`bridge_swarm_memberships.display_name_set_by_host BOOLEAN NOT NULL DEFAULT
FALSE` (via `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`, the same idiom every
prior schema change in this file uses). `display_name` itself already
existed (ADR 0018) — this only adds the flag that distinguishes "nobody has
set anything yet, or a Bridge published a name" from "the Host owner set
this deliberately."

`SetBridgeDisplayName` (the existing Host-owner-facing method,
`host/directory/bridges.go`) now also sets this flag: `true` whenever the
new name is non-empty, `false` when the Host owner clears it back to empty
— that second case is exactly what reopens the slot described in
sub-decision 1.

### Precedence, enforced in one `UPDATE ... WHERE`

`Directory.PublishInventory` gained a `bridgeName string` parameter. After
the manifest itself is successfully replaced, if `bridgeName != ""`:

```sql
UPDATE bridge_swarm_memberships
SET display_name = $1
WHERE swarm_id = $2 AND bridge_id = $3 AND display_name_set_by_host = FALSE
```

The `WHERE display_name_set_by_host = FALSE` clause *is* the precedence
rule — no read-then-branch-then-write, no risk of a lost-update race between
checking the flag and writing the name. A Host-set name simply never
matches the `WHERE` and the statement becomes a no-op for that row.

### Wire format

`host/hostapi`'s `publishInventoryRequest` (and `bridge/hostclient`'s
matching `publishInventoryRequest`) both gained `DisplayName string
\`json:"display_name,omitempty"\``. Empty/omitted means exactly what it
already meant before this ADR: the Bridge isn't publishing a name, and
whatever the Host-side label already was (Host-set, previously
Bridge-published, or still blank) is left untouched.

### Bridge side

`bridgeconfig.Config` gained `DisplayName string`, editable through a new
field on the Settings page (`bridge/adminui/templates/settings.html`,
`handleSaveConnection`). `Daemon.PublishInventory`
(`cmd/bridge/inventory.go`) loads this config value at publish time (a
second, independent load from `ConfigStore()`, alongside the existing
`swarmStore` load — the RomM-connection config and the Swarm-connection
config are deliberately separate stores, see `bridgeconfig`'s own package
doc) and threads it through to `hostclient.PublishInventory`. A load
failure here is non-fatal to publishing — it just means no name is sent
this round, identical in effect to the field being left blank.

## Consequences

- A Bridge's name can change any time by editing Settings — no
  re-enrollment, no Host-side action required, as long as the Host owner
  hasn't pinned a name of their own.
- The Host owner retains a real "lock" on a Bridge's displayed name:
  setting one sticks until they explicitly clear it, exactly matching ADR
  0018's original guarantee for as long as they choose to exercise it.
- `bridge_swarm_memberships` gains one boolean column; no new table, no
  change to `inventory_snapshots`/`inventory_items`.

## Explicitly out of scope

- Per-Swarm distinct display names for a Bridge that has joined more than
  one Swarm — `cmd/bridge`'s `Daemon` supports exactly one Swarm connection
  today (the same limitation ADR 0019 already inherited and named), so one
  `DisplayName` field is sufficient for now.
- Validating or sanitizing the Bridge-chosen name beyond what the Host
  already does for any stored string (length limits via the existing
  request body cap, no HTML/script injection risk since `host/hostui`
  already escapes all rendered fields by default via Go's `html/template`).
- Notifying a Bridge owner when the Host owner has overridden their chosen
  name — the Bridge has no way to observe the Host-side flag today; it
  simply stops seeing its own name take effect. Worth a future look if it
  proves confusing in practice.

## Open questions

- Whether the Bridge's own admin UI should show "your name may not be in
  effect — the Host owner has set a different one" once some way exists for
  the Bridge to read back `display_name_set_by_host`. Not built here: no
  existing route surfaces per-Swarm membership state back to a Bridge.
