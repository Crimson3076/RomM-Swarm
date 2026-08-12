package directory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/reference"
)

// ErrUnsupportedPlatform means platform is not one of protocol.InitialPlatforms().
var ErrUnsupportedPlatform = errors.New("directory: platform is not one of the supported platforms")

// ReferenceCatalogue is one Swarm's currently-stored catalogue for one
// platform, as returned to a caller. Content only carries the raw DAT
// bytes when the caller actually needs them — GetReferenceCatalogues, for
// Bridge distribution; ListReferenceCatalogues leaves it nil, since the
// Host UI's listing only needs the metadata to render a table, not the
// content itself (which can run to a few megabytes).
type ReferenceCatalogue struct {
	Platform      protocol.PlatformID
	Filename      string
	ContentSHA256 string
	EntryCount    int
	UploadedAt    time.Time
	Content       []byte
}

func isSupportedPlatform(platform protocol.PlatformID) bool {
	for _, p := range protocol.InitialPlatforms() {
		if p == platform {
			return true
		}
	}
	return false
}

// UploadReferenceCatalogue validates content as a Logiqx DAT for platform —
// reusing bridge/reference's own parser, so a bad upload is rejected
// immediately with a clear error rather than silently failing on every
// Bridge's next fetch — and stores it as swarm's current catalogue for
// that platform, replacing whatever was there before. There is no upload
// history, only the current one: ADR 0023 made the Host the sole
// authority for verification per platform, so a stale prior version
// serves no purpose once replaced.
func (d *Directory) UploadReferenceCatalogue(ctx context.Context, account protocol.UserID, swarm protocol.SwarmID, platform protocol.PlatformID, filename string, content []byte) error {
	if _, err := d.GetSwarm(ctx, account, swarm); err != nil {
		return err
	}
	if !isSupportedPlatform(platform) {
		return ErrUnsupportedPlatform
	}

	set, err := reference.ImportDAT(bytes.NewReader(content), reference.ImportOptions{Platform: platform})
	if err != nil {
		return fmt.Errorf("directory: %s does not parse as a reference catalogue for %s: %w", filename, platform, err)
	}

	now := d.now()
	if _, err := d.DB.ExecContext(ctx, `
		INSERT INTO swarm_reference_catalogues (swarm_id, platform, filename, dat_content, content_sha256, entry_count, uploaded_by, uploaded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (swarm_id, platform) DO UPDATE SET
			filename = EXCLUDED.filename,
			dat_content = EXCLUDED.dat_content,
			content_sha256 = EXCLUDED.content_sha256,
			entry_count = EXCLUDED.entry_count,
			uploaded_by = EXCLUDED.uploaded_by,
			uploaded_at = EXCLUDED.uploaded_at`,
		string(swarm), string(platform), filename, content, set.ImportDigest.SHA256, len(set.Entries), string(account), now,
	); err != nil {
		return fmt.Errorf("directory: storing reference catalogue: %w", err)
	}

	_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventReferenceImported, At: now, SwarmID: swarm, ActorUser: account})
	return nil
}

// ListReferenceCatalogues returns swarm's currently-loaded catalogues,
// metadata only — the Host UI's own listing, scoped to account's
// membership the same way every other owner-facing Swarm read is.
func (d *Directory) ListReferenceCatalogues(ctx context.Context, account protocol.UserID, swarm protocol.SwarmID) ([]ReferenceCatalogue, error) {
	if _, err := d.GetSwarm(ctx, account, swarm); err != nil {
		return nil, err
	}

	rows, err := d.DB.QueryContext(ctx, `
		SELECT platform, filename, content_sha256, entry_count, uploaded_at
		FROM swarm_reference_catalogues
		WHERE swarm_id = $1
		ORDER BY platform`, string(swarm))
	if err != nil {
		return nil, fmt.Errorf("directory: listing reference catalogues: %w", err)
	}
	defer rows.Close()

	var out []ReferenceCatalogue
	for rows.Next() {
		var c ReferenceCatalogue
		if err := rows.Scan(&c.Platform, &c.Filename, &c.ContentSHA256, &c.EntryCount, &c.UploadedAt); err != nil {
			return nil, fmt.Errorf("directory: reading reference catalogue row: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("directory: listing reference catalogues: %w", err)
	}
	return out, nil
}

// DeleteReferenceCatalogue removes swarm's stored catalogue for platform,
// if any. Not an error when none was stored — deleting something already
// absent reaches the same end state as deleting it now, the same
// tolerance RemoveBridgeFromSwarm-adjacent operations in this package
// apply elsewhere.
func (d *Directory) DeleteReferenceCatalogue(ctx context.Context, account protocol.UserID, swarm protocol.SwarmID, platform protocol.PlatformID) error {
	if _, err := d.GetSwarm(ctx, account, swarm); err != nil {
		return err
	}
	if _, err := d.DB.ExecContext(ctx, `
		DELETE FROM swarm_reference_catalogues WHERE swarm_id = $1 AND platform = $2`,
		string(swarm), string(platform)); err != nil {
		return fmt.Errorf("directory: deleting reference catalogue: %w", err)
	}
	return nil
}

// GetReferenceCatalogues authenticates bridge and returns every reference
// catalogue currently stored for swarm, content included — the
// Bridge-facing read Daemon.PublishInventory calls before every publish
// attempt (ADR 0023), so an update to the Host's catalogue reaches
// already-joined Bridges on their own, without requiring them to leave
// and rejoin.
func (d *Directory) GetReferenceCatalogues(ctx context.Context, bridge protocol.BridgeID, presented auth.Token, swarm protocol.SwarmID) ([]ReferenceCatalogue, error) {
	if err := d.verifier.Authenticate(bridge, presented); err != nil {
		_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventAuthFailure, At: d.now(), ActorBridge: bridge})
		return nil, err
	}
	if err := d.requireActiveMembership(ctx, bridge, swarm); err != nil {
		return nil, err
	}

	rows, err := d.DB.QueryContext(ctx, `
		SELECT platform, filename, dat_content, content_sha256, entry_count, uploaded_at
		FROM swarm_reference_catalogues
		WHERE swarm_id = $1
		ORDER BY platform`, string(swarm))
	if err != nil {
		return nil, fmt.Errorf("directory: fetching reference catalogues: %w", err)
	}
	defer rows.Close()

	var out []ReferenceCatalogue
	for rows.Next() {
		var c ReferenceCatalogue
		if err := rows.Scan(&c.Platform, &c.Filename, &c.Content, &c.ContentSHA256, &c.EntryCount, &c.UploadedAt); err != nil {
			return nil, fmt.Errorf("directory: reading reference catalogue row: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("directory: fetching reference catalogues: %w", err)
	}
	return out, nil
}
