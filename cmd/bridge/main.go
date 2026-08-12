// Command bridge is the persistent Bridge daemon — ADR 0015. Unlike
// cmd/swarm-bridge (a one-shot CLI kept unchanged for quick manual testing),
// this is meant to run continuously: it loads or seeds a persistent config,
// connects to the operator's own RomM server, and serves a local admin web
// UI (bridge/adminui) for managing that connection, credentials, imports,
// and activity — even before a RomM connection exists, since /setup is how
// an operator without ROMM_URL/ROMM_TOKEN env vars configures one.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/adminui"
	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
)

// abandonedStagingAge is how long an interrupted transfer's staged file is
// left alone before CleanupAbandoned reclaims it — generous relative to
// protocol.DefaultIngestionTimeout so a slow-but-live transfer is never
// mistaken for an abandoned one.
const abandonedStagingAge = 6 * time.Hour

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check GET /healthz on -addr and exit 0/1 (used by Docker HEALTHCHECK; no shell in the image)")
	addr := flag.String("addr", envOr("BRIDGE_LISTEN_ADDR", ":8080"), "address to serve the admin UI on")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck(*addr))
	}

	configDir := envOr("BRIDGE_CONFIG_DIR", "./data")
	inboxDir := strings.TrimSpace(os.Getenv("BRIDGE_INBOX_DIR"))

	d, err := NewDaemon(configDir)
	if err != nil {
		fatal(err)
	}

	bootstrapCtx, cancelBootstrap := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := d.Bootstrap(bootstrapCtx); err != nil {
		fmt.Fprintf(os.Stderr, "bridge: %v\n", err)
	}
	if err := d.BootstrapSwarm(bootstrapCtx); err != nil {
		fmt.Fprintf(os.Stderr, "bridge: %v\n", err)
	}
	cancelBootstrap()

	cfg, err := d.ConfigStore().Load()
	if err != nil && !errors.Is(err, bridgeconfig.ErrNotConfigured) {
		fatal(err)
	}
	if cfg.Configured() {
		connectCtx, cancelConnect := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := d.Reconnect(connectCtx); err != nil {
			fmt.Fprintf(os.Stderr, "bridge: could not connect to %s: %v (the admin UI's Settings page can retry)\n", cfg.RommURL, err)
		} else {
			fmt.Printf("bridge: connected to %s\n", cfg.RommURL)
			// Fills the in-memory Library cache before anyone asks for it —
			// see WarmLibraryCache's own doc comment for why this matters
			// specifically right after a restart.
			d.WarmLibraryCache()
		}
		cancelConnect()
	} else {
		fmt.Printf("bridge: config dir %s ready, not yet configured — visit the admin UI to run /setup.\n", configDir)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stopCleanup := startAbandonedStagingCleanup(d)
	defer stopCleanup()

	// Auto-publish (ADR 0019's follow-on): started unconditionally, not
	// just when already joined at boot — a Bridge can join a Swarm later
	// through the admin UI, and this loop picks that up on its own without
	// needing a restart (see StartAutoPublish's own doc comment).
	stopAutoPublish := d.StartAutoPublish(ctx, publishIntervalFromEnv())
	defer stopAutoPublish()

	server := adminui.New(d, inboxDir)
	httpServer := &http.Server{Addr: *addr, Handler: server}

	serveErr := make(chan error, 1)
	go func() {
		fmt.Printf("bridge: serving the admin UI on %s\n", *addr)
		serveErr <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(err)
		}
	case <-ctx.Done():
		fmt.Println("bridge: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "bridge: error during shutdown: %v\n", err)
		}
	}
}

// startAbandonedStagingCleanup periodically reclaims staged files an
// interrupted transfer left behind, so a crash mid-import doesn't leak disk
// forever. Returns a stop func to cancel the ticker on shutdown.
func startAbandonedStagingCleanup(d *Daemon) func() {
	ticker := time.NewTicker(1 * time.Hour)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				if n, err := d.staging.CleanupAbandoned(abandonedStagingAge, time.Now()); err != nil {
					log.Printf("bridge: abandoned-staging cleanup: %v", err)
				} else if n > 0 {
					log.Printf("bridge: abandoned-staging cleanup removed %d file(s)", n)
				}
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// runHealthcheck is what `bridge -healthcheck` does for Docker's
// HEALTHCHECK: the distroless runtime image has no shell, so the binary
// checks itself rather than something shelling out to curl/wget.
func runHealthcheck(addr string) int {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bridge: healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "bridge: healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
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
