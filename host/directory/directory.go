// Package directory is the Network Host's domain logic — ADR 0016, slice 1.
// It composes host/hoststore (persistence) and auth.Verifier (the Bridge
// credential protocol) into account, Swarm, invitation, and Bridge
// enrollment/rotation/revocation operations.
package directory

import (
	"database/sql"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
)

// Directory is the Host's domain-logic entry point.
type Directory struct {
	DB *sql.DB

	// Now overrides the clock, for tests. Nil means time.Now.
	Now func() time.Time

	verifier  *auth.Verifier
	events    *hoststore.EventStore
	inventory *hoststore.InventoryStore
	locks     bridgeLocks
}

// New returns a Directory backed by db. The schema must already be applied
// (see hoststore.ApplySchema).
func New(db *sql.DB) *Directory {
	return &Directory{
		DB:        db,
		verifier:  &auth.Verifier{Store: &hoststore.BridgeCredentialStore{DB: db}},
		events:    &hoststore.EventStore{DB: db},
		inventory: &hoststore.InventoryStore{DB: db},
	}
}

func (d *Directory) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
