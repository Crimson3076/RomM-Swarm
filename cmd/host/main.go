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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hostapi"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
	"github.com/Crimson3076/RomM-Swarm/host/hostui"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check GET /healthz on -addr and -ui-addr and exit 0/1 (used by Docker HEALTHCHECK; no shell in the image)")
	addr := flag.String("addr", envOr("HOST_LISTEN_ADDR", ":8081"), "address to serve the Host JSON API on")
	uiAddr := flag.String("ui-addr", envOr("HOST_UI_LISTEN_ADDR", ":8082"), "address to serve the Host web UI on")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck(*addr, *uiAddr))
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

	dir := directory.New(db)
	apiHandler := hostapi.New(dir)
	if raw := strings.TrimSpace(os.Getenv("HOST_MAX_INVENTORY_BYTES")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			apiHandler.MaxInventoryBytes = n
		} else {
			fmt.Fprintf(os.Stderr, "host: ignoring invalid HOST_MAX_INVENTORY_BYTES=%q, using the default\n", raw)
		}
	}
	apiServer := &http.Server{Addr: *addr, Handler: apiHandler}
	uiServer := &http.Server{Addr: *uiAddr, Handler: hostui.New(dir)}

	// Two independent servers over one Directory: hostapi.Server is
	// deliberately JSON-only (see its own package doc comment), and
	// hostui.Server is its own browser-facing front end on a separate
	// port — the same relationship bridge/adminui has to cmd/bridge's
	// daemon core.
	serveErr := make(chan error, 2)
	go func() {
		fmt.Printf("host: serving the API on %s\n", *addr)
		serveErr <- apiServer.ListenAndServe()
	}()
	go func() {
		fmt.Printf("host: serving the web UI on %s\n", *uiAddr)
		serveErr <- uiServer.ListenAndServe()
	}()

	shutdown := func() {
		fmt.Println("host: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := apiServer.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "host: error shutting down the API server: %v\n", err)
		}
		if err := uiServer.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "host: error shutting down the web UI server: %v\n", err)
		}
	}

	select {
	case err := <-serveErr:
		shutdown()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(err)
		}
	case <-ctx.Done():
		shutdown()
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
