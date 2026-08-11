// Package hoststore is the Network Host's PostgreSQL persistence layer —
// ADR 0016, slice 1 (identity, Swarms, memberships, invitations, Bridge
// enrollment). This is the one package in the repository allowed to depend
// on a Postgres driver; nothing under bridge/ or cmd/bridge may import it,
// so Bridge's dependency-light build stays exactly as it was before this
// package existed.
//
// Concurrency note, recorded here because it governs how every repository
// type in this package is written: nothing in hoststore ever holds a
// transaction open across two separate Go-level calls. Every method is a
// single statement, atomic on its own by ordinary PostgreSQL guarantees.
// Cross-call atomicity — the property auth.Store's own doc comment calls
// for — is the caller's job (host/directory's per-BridgeID mutex), not
// this package's. See ADR 0016, resolved sub-decision 1, for why the
// obvious alternative (a transaction held from Load to Save) is wrong.
package hoststore

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaSQL string

// Open connects to Postgres via the given DSN. It does not apply the
// schema — call ApplySchema separately, so a caller can choose when
// schema changes happen relative to the rest of startup.
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("hoststore: opening database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("hoststore: connecting to database: %w", err)
	}
	return db, nil
}

// ApplySchema creates every table this slice needs, idempotently. Safe to
// call on every boot.
func ApplySchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("hoststore: applying schema: %w", err)
	}
	return nil
}
