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

-- Host-assigned, per-Swarm label so an owner managing several Bridges can
-- tell them apart by something more legible than a BridgeID. Deliberately
-- not Bridge-self-declared: a Bridge-supplied name would be
-- attacker-controlled text landing directly in the owner's own dashboard.
-- Added via ALTER rather than in the CREATE TABLE above so applying the
-- schema against an already-running database (no migration tool yet;
-- see the file comment) never requires dropping existing data.
ALTER TABLE bridge_swarm_memberships ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';

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

-- No sweeper reads or deletes from this table yet; see the file comment.
CREATE TABLE IF NOT EXISTS events (
    id           BIGSERIAL PRIMARY KEY,
    kind         TEXT NOT NULL,
    at           TIMESTAMPTZ NOT NULL,
    swarm_id     TEXT,
    actor_user   TEXT,
    actor_bridge TEXT
);
