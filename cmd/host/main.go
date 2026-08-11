// Command host is the Network Host daemon — ADR 0016, slice 1: accounts,
// Swarms, memberships, invitations, and Bridge enrollment/rotation/
// revocation. It is not cross-Bridge federation: the central inventory
// index, transfer grants, and everything else Scope of Work §5.1 lists
// remain unimplemented (see ADR 0016's "explicitly out of scope" section).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hostapi"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check GET /healthz on -addr and exit 0/1 (used by Docker HEALTHCHECK; no shell in the image)")
	addr := flag.String("addr", envOr("HOST_LISTEN_ADDR", ":8081"), "address to serve the Host API on")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck(*addr))
	}

	dsn := strings.TrimSpace(os.Getenv("HOST_DATABASE_URL"))
	if dsn == "" {
		fatal(fmt.Errorf("HOST_DATABASE_URL is not set"))
	}

	db, err := hoststore.Open(dsn)
	if err != nil {
		fatal(err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := hoststore.ApplySchema(ctx, db); err != nil {
		fatal(err)
	}
	fmt.Println("host: schema applied")

	server := hostapi.New(directory.New(db))
	httpServer := &http.Server{Addr: *addr, Handler: server}

	serveErr := make(chan error, 1)
	go func() {
		fmt.Printf("host: serving the API on %s\n", *addr)
		serveErr <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(err)
		}
	case <-ctx.Done():
		fmt.Println("host: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "host: error during shutdown: %v\n", err)
		}
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "host: "+err.Error())
	os.Exit(1)
}
