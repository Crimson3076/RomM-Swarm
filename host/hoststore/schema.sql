-- Schema for ADR 0016's identity slice: accounts, sessions, Swarms,
-- memberships, invitations, Bridges, Bridge credential families, and a
-- minimal event log with no retention sweeper (ADR 0006 is still Proposed;
-- the sweeper is out of scope until its retention-window questions are
-- settled — see ADR 0016's "explicitly out of scope" section).
--
-- Applied idempotently via ApplySchema; no external migration tool yet.

CREATE TABLE IF NOT EXISTS accounts (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL,
    email         TEXT,
    password_hash TEXT NOT NULL,
    is_owner      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL,
    disabled_at   TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS swarms (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    alias_key  BYTEA NOT NULL,
    created_by TEXT NOT NULL REFERENCES accounts(id),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS swarm_memberships (
    swarm_id   TEXT NOT NULL REFERENCES swarms(id),
    account_id TEXT NOT NULL REFERENCES accounts(id),
    role       TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    joined_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (swarm_id, account_id)
);

CREATE TABLE IF NOT EXISTS invitations (
    id         TEXT PRIMARY KEY,
    swarm_id   TEXT NOT NULL REFERENCES swarms(id),
    code_hash  TEXT NOT NULL UNIQUE,
    issued_by  TEXT NOT NULL REFERENCES accounts(id),
    max_uses   INTEGER NOT NULL DEFAULT 1,
    use_count  INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS bridges (
    id                  TEXT PRIMARY KEY,
    public_identity_key BYTEA NOT NULL UNIQUE,
    created_at          TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS bridge_swarm_memberships (
    bridge_id               TEXT NOT NULL REFERENCES bridges(id),
    swarm_id                TEXT NOT NULL REFERENCES swarms(id),
    enrolled_via_invitation TEXT REFERENCES invitations(id),
    state                   TEXT NOT NULL DEFAULT 'enrolled',
    joined_at               TIMESTAMPTZ NOT NULL,
    disabled_at             TIMESTAMPTZ,
    revoked_at              TIMESTAMPTZ,
    revoked_reason          TEXT,
    PRIMARY KEY (bridge_id, swarm_id)
);

-- Per-Swarm label so an owner managing several Bridges can tell them apart
-- by something more legible than a BridgeID. A Bridge may publish its own
-- chosen name with every inventory publish (ADR 0022) -- text a Bridge's
-- own operator controls, landing in this Swarm owner's dashboard, so the
-- Host owner always has the final word: display_name_set_by_host tracks
-- whether the Host owner has manually set (or is still deferring on) the
-- name, and a Bridge-declared name is only ever applied while that's
-- false. Setting it non-empty through the Host UI flips the flag true and
-- locks out further Bridge-declared updates; clearing it back to empty
-- flips the flag false again, reopening it to the Bridge's own name.
-- Added via ALTER rather than in the CREATE TABLE above so applying the
-- schema against an already-running database (no migration tool yet;
-- see the file comment) never requires dropping existing data.
ALTER TABLE bridge_swarm_memberships ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE bridge_swarm_memberships ADD COLUMN IF NOT EXISTS display_name_set_by_host BOOLEAN NOT NULL DEFAULT FALSE;

-- Mirrors auth.FamilyState exactly. See BridgeCredentialStore.
CREATE TABLE IF NOT EXISTS bridge_credential_families (
    bridge_id          TEXT PRIMARY KEY REFERENCES bridges(id),
    current_hash       TEXT NOT NULL,
    previous_hash      TEXT NOT NULL DEFAULT '',
    previous_issued_at TIMESTAMPTZ,
    previous_consumed  BOOLEAN NOT NULL DEFAULT FALSE,
    generation         BIGINT NOT NULL DEFAULT 0,
    revoked            BOOLEAN NOT NULL DEFAULT FALSE,
    revoked_reason     TEXT NOT NULL DEFAULT '',
    updated_at         TIMESTAMPTZ NOT NULL
);

-- ADR 0019, slice 1: latest inventory snapshot metadata per (Bridge,
-- Swarm). No history, no Delta replay -- a republish overwrites the
-- previous snapshot wholesale, via InventoryStore.Replace's transaction.
CREATE TABLE IF NOT EXISTS inventory_snapshots (
    bridge_id     TEXT NOT NULL REFERENCES bridges(id),
    swarm_id      TEXT NOT NULL REFERENCES swarms(id),
    revision      BIGINT NOT NULL,
    item_count    INTEGER NOT NULL,
    total_bytes   BIGINT NOT NULL,
    fingerprints  JSONB NOT NULL DEFAULT '{}',
    generated_at  TIMESTAMPTZ NOT NULL,
    published_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (bridge_id, swarm_id)
);

-- One row per published item, replaced wholesale on every publish. This
-- deliberately carries only what slice-1 cross-Bridge stats need (file id,
-- platform, size) -- not the full protocol.Item (no classification,
-- reference match, container, or adapter detail). That is the "central
-- inventory index" ADR 0016 named out of scope; this is a narrow,
-- purpose-built stats table, not it. See ADR 0019.
CREATE TABLE IF NOT EXISTS inventory_items (
    bridge_id      TEXT NOT NULL REFERENCES bridges(id),
    swarm_id       TEXT NOT NULL REFERENCES swarms(id),
    file_id        TEXT NOT NULL,
    platform       TEXT NOT NULL,
    canonical_size BIGINT NOT NULL,
    PRIMARY KEY (bridge_id, swarm_id, file_id)
);
CREATE INDEX IF NOT EXISTS inventory_items_by_swarm_file ON inventory_items (swarm_id, file_id);

-- ADR 0023: one reference catalogue per (Swarm, platform), uploaded by the
-- Swarm owner through the Host UI and distributed to every joined Bridge.
-- The Host is the sole authority here -- a Bridge cannot supply its own
-- catalogue for a platform the Host has one for (see the ADR's precedence
-- discussion), so there is exactly one row per platform per Swarm, not a
-- history of uploads.
CREATE TABLE IF NOT EXISTS swarm_reference_catalogues (
    swarm_id       TEXT NOT NULL REFERENCES swarms(id),
    platform       TEXT NOT NULL,
    filename       TEXT NOT NULL,
    dat_content    BYTEA NOT NULL,
    content_sha256 TEXT NOT NULL,
    entry_count    INTEGER NOT NULL,
    uploaded_by    TEXT NOT NULL REFERENCES accounts(id),
    uploaded_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (swarm_id, platform)
);

-- No sweeper reads or deletes from this table yet; see the file comment.
CREATE TABLE IF NOT EXISTS events (
    id           BIGSERIAL PRIMARY KEY,
    kind         TEXT NOT NULL,
    at           TIMESTAMPTZ NOT NULL,
    swarm_id     TEXT,
    actor_user   TEXT,
    actor_bridge TEXT
);
