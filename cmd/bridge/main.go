// Command bridge is the persistent Bridge daemon — ADR 0015. Unlike
// cmd/swarm-bridge (a one-shot CLI kept unchanged for quick manual testing),
// this is meant to run continuously: it loads or seeds a persistent config,
// connects to the operator's own RomM server, and (from the next stage of
// ADR 0015 onward) serves a local admin web UI over that connection.
//
// This stage wires the daemon's non-HTTP core only — config, connection,
// and the import pipeline — so it can be verified against a real RomM
// server before the web layer is built on top of it. Run it and it reports
// its own status and exits; the admin UI (bridge/adminui) is what will make
// it a true long-running process.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
)

func main() {
	configDir := envOr("BRIDGE_CONFIG_DIR", "./data")

	d, err := NewDaemon(configDir)
	if err != nil {
		fatal(err)
	}

	bootstrapCtx, cancelBootstrap := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := d.Bootstrap(bootstrapCtx); err != nil {
		fmt.Fprintf(os.Stderr, "bridge: %v\n", err)
	}
	cancelBootstrap()

	cfg, err := d.ConfigStore.Load()
	if err != nil && !errors.Is(err, bridgeconfig.ErrNotConfigured) {
		fatal(err)
	}

	if !cfg.Configured() {
		fmt.Printf("bridge: config dir %s ready, not yet configured.\n", configDir)
		fmt.Println("bridge: set ROMM_URL and ROMM_TOKEN to seed a first configuration, or use the admin UI's setup flow once it exists.")
		return
	}

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelConnect()
	if err := d.Reconnect(connectCtx); err != nil {
		fmt.Fprintf(os.Stderr, "bridge: could not connect to %s: %v\n", cfg.RommURL, err)
		os.Exit(1)
	}

	fmt.Printf("bridge: config dir %s ready, connected to %s.\n", configDir, cfg.RommURL)
	fmt.Println("bridge: admin UI is not wired up yet in this build.")
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "bridge: "+err.Error())
	os.Exit(1)
}
