// Package hoststoretest provides the Postgres-gated test convention shared
// by every package under host/ that needs a real database — mirrors the
// standard library's own httptest/iotest/fstest split: test-only helpers
// that need "testing" live in their own package so the production package
// they help test never imports it.
package hoststoretest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/host/hoststore"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DSNEnv is the environment variable that gates every Postgres-backed test
// under host/. Unset: the whole suite stays runnable with zero external
// services. Set to a real DSN: these tests run against it, each in its own
// throwaway schema.
const DSNEnv = "TEST_POSTGRES_DSN"

// SkipWithoutPostgres skips the calling test unless DSNEnv is set,
// returning the DSN when it is.
func SkipWithoutPostgres(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(DSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run this package's Postgres-backed tests", DSNEnv)
	}
	return dsn
}

// OpenDB returns a *sql.DB pinned to a single connection (so a
// session-scoped SET search_path stays in effect for every statement
// issued through it) with the schema applied into a fresh, isolated
// Postgres schema, and registers cleanup to drop that schema and close the
// connection.
func OpenDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("hoststoretest: opening test database: %v", err)
	}
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("hoststoretest: connecting to test database: %v", err)
	}

	schema := "test_" + randomSuffix()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		db.Close()
		t.Fatalf("hoststoretest: creating test schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "SET search_path TO "+schema+", public"); err != nil {
		db.Close()
		t.Fatalf("hoststoretest: setting search_path: %v", err)
	}
	if err := hoststore.ApplySchema(ctx, db); err != nil {
		db.Close()
		t.Fatalf("hoststoretest: applying schema to test database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		db.Close()
	})

	return db
}

func randomSuffix() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("hoststoretest: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}
