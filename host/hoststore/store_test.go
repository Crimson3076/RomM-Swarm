package hoststore_test

import (
	"context"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
)

// TestPhase2_SchemaApplyIsIdempotent proves ApplySchema (already exercised
// once per test by hoststoretest.OpenDB) can run a second time against the
// same schema without error — every boot of cmd/host calls it, not just
// the first.
func TestPhase2_SchemaApplyIsIdempotent(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)

	if err := hoststore.ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("second ApplySchema failed: %v", err)
	}
}

// TestOpenRejectsAnUnreachableDSN proves Open fails fast (via Ping) rather
// than deferring the failure to the first real query.
func TestOpenRejectsAnUnreachableDSN(t *testing.T) {
	_, err := hoststore.Open("postgres://nobody@127.0.0.1:1/does-not-exist?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("Open succeeded against an unreachable DSN")
	}
}
