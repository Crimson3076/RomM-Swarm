package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Bootstrap seeds a fresh config from ROMM_URL/ROMM_TOKEN (and, optionally,
// BRIDGE_DESTINATION_MODE/BRIDGE_LIBRARY_ROOT), but only once: if
// config.json already exists — configured or not — it is left untouched and
// the environment is not consulted again on a later boot. This is the
// Docker-convenience path for a first run; the admin UI's /setup flow is
// the other way in, and either is sufficient on its own.
//
// A no-op, not an error, when no config file exists yet but the env vars
// aren't set either — that just means /setup will do the configuring.
func (d *Daemon) Bootstrap(ctx context.Context) error {
	_, err := d.ConfigStore.Load()
	if err == nil {
		return nil // already configured (or already deliberately left unconfigured)
	}
	if !errors.Is(err, bridgeconfig.ErrNotConfigured) {
		return err
	}

	url := strings.TrimSpace(os.Getenv("ROMM_URL"))
	token := strings.TrimSpace(os.Getenv("ROMM_TOKEN"))
	if url == "" || token == "" {
		return nil
	}

	conn, err := connect(ctx, url, token)
	if err != nil {
		return fmt.Errorf("could not seed configuration from ROMM_URL/ROMM_TOKEN: %w", err)
	}

	cfg := bridgeconfig.Config{RommURL: url, RommToken: token}
	if mode := strings.TrimSpace(os.Getenv("BRIDGE_DESTINATION_MODE")); mode != "" {
		cfg.DestinationMode = protocol.DestinationMode(mode)
	}
	if root := strings.TrimSpace(os.Getenv("BRIDGE_LIBRARY_ROOT")); root != "" {
		cfg.LibraryRoot = root
	}
	if err := d.ConfigStore.Save(cfg); err != nil {
		return fmt.Errorf("seeding configuration: %w", err)
	}
	d.conn.Store(conn)
	return nil
}
