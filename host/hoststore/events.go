package hoststore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// EventStore records events into the events table. No sweeper reads from
// it yet — see schema.sql's file comment and ADR 0016's "explicitly out of
// scope" section; ADR 0006's retention-window questions are still open and
// genuinely block that part, unlike everything else in this slice.
type EventStore struct {
	DB *sql.DB
}

// Record validates ev (rejecting an unknown EventKind, per host/README.md's
// stated rule) and appends it.
func (s *EventStore) Record(ctx context.Context, ev protocol.Event) error {
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("hoststore: refusing to record an invalid event: %w", err)
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO events (kind, at, swarm_id, actor_user, actor_bridge)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''))`,
		string(ev.Kind), ev.At, string(ev.SwarmID), string(ev.ActorUser), string(ev.ActorBridge))
	if err != nil {
		return fmt.Errorf("hoststore: recording event %s: %w", ev.Kind, err)
	}
	return nil
}
