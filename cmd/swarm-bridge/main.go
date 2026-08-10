// Command swarm-bridge is a minimal, single-Bridge command line for actually
// exercising the pieces Phase 0 has proven, against a real RomM server.
//
// It is not the Phase 1 Bridge. There is no persistent config, no Swarm
// membership, no second machine, no database — just the already-tested
// building blocks (bridge/scan, bridge/ingest, bridge/romm) wired into two
// commands you can run today:
//
//	ROMM_URL=https://romm.example ROMM_TOKEN=... swarm-bridge list gb
//	ROMM_URL=https://romm.example ROMM_TOKEN=... swarm-bridge upload gb ./game.gb
//
// list reads RomM's own inventory for a platform. upload runs the real
// receiving flow end to end: it stages the file, verifies it, hands it to
// RomM's chunked upload API, and waits for RomM's own filesystem watcher to
// index and match it — the same ingest.Flow that Phase 6 will drive, run for
// real for the first time here rather than only against fakes in tests.
//
// upload writes to your real RomM library. There is nothing simulated about
// it once it starts.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/destination"
	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/verify"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "list":
		runList(os.Args[2:])
	case "upload":
		runUpload(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `swarm-bridge: exercise the Phase 0 RomM pipeline against a real server

Usage:
  swarm-bridge list <platform-slug>
  swarm-bridge upload [-timeout 30m] <platform-slug> <local-file>

Requires ROMM_URL and ROMM_TOKEN in the environment.`)
	os.Exit(1)
}

func env() (baseURL, token string) {
	baseURL = strings.TrimSpace(os.Getenv("ROMM_URL"))
	token = strings.TrimSpace(os.Getenv("ROMM_TOKEN"))
	if baseURL == "" {
		fail("ROMM_URL is not set. Point it at your RomM server, for example https://romm.example")
	}
	if token == "" {
		fail("ROMM_TOKEN is not set. A Client API Token is required for both listing and uploading.")
	}
	return baseURL, token
}

// connect probes the server and returns a ready-to-use client and report, the
// same sequence every tool in this repository follows: probe first, then
// build every subsequent request from what the probe actually resolved.
func connect(ctx context.Context, baseURL, token string) (*romm.Client, *romm.Report) {
	report, err := romm.Probe(ctx, romm.ProbeOptions{BaseURL: baseURL, Token: token})
	if err != nil {
		// Probe only returns an error for a malformed call (e.g. no URL at
		// all); every network or server-side failure instead lands in
		// report.Blockers/Findings below, deliberately, so a partial probe is
		// still informative — see romm.Probe's own doc comment.
		fail(fmt.Sprintf("probing %s: %v", baseURL, err))
	}
	if len(report.Blockers) > 0 {
		fail(fmt.Sprintf("could not reach %s: %s", baseURL, strings.Join(report.Blockers, "; ")))
	}
	if missing := romm.MissingRequired(report.Capabilities); len(missing) > 0 {
		ids := make([]string, len(missing))
		for i, c := range missing {
			ids[i] = c.ID
		}
		fail(fmt.Sprintf("the server is missing required capabilities: %s", strings.Join(ids, ", ")))
	}
	if report.AuthScheme == "" {
		fail("the server did not accept the credential — check ROMM_TOKEN")
	}
	scheme, ok := romm.LookupAuthScheme(report.AuthScheme)
	if !ok {
		fail(fmt.Sprintf("the probe accepted the credential under scheme %q, which this build does not recognise", report.AuthScheme))
	}
	return romm.NewClient(baseURL, token, scheme), report
}

func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
	}
	platformSlug := fs.Arg(0)

	baseURL, token := env()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, report := connect(ctx, baseURL, token)
	source := &scan.RommSource{Client: client, Report: report}

	fmt.Printf("Listing %s on %s\n\n", platformSlug, baseURL)

	offset, total, printed := 0, 0, 0
	for {
		page, hasMore, err := source.ListROMs(ctx, scan.Page{Limit: 100, Offset: offset})
		if err != nil {
			fail(fmt.Sprintf("listing RomM's inventory: %v", err))
		}
		for _, rec := range page {
			total++
			if rec.PlatformSlug != platformSlug {
				continue
			}
			printed++
			fmt.Printf("  %-8s %-50s %10d bytes\n", rec.ID, rec.FSName, rec.FSSizeBytes)
		}
		if !hasMore || len(page) == 0 {
			break
		}
		offset += len(page)
	}

	fmt.Printf("\n%d item(s) on %s, out of %d scanned across all platforms.\n", printed, platformSlug, total)
}

func runUpload(args []string) {
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	timeout := fs.Duration("timeout", protocol.DefaultIngestionTimeout, "how long to wait for RomM to ingest and match the upload")
	fs.Parse(args)
	if fs.NArg() != 2 {
		usage()
	}
	platformSlug, localPath := fs.Arg(0), fs.Arg(1)

	baseURL, token := env()

	fmt.Printf("Analysing %s as %s...\n", localPath, platformSlug)
	res, err := (&verify.Analyzer{}).AnalyzeFile(localPath, protocol.PlatformID(platformSlug))
	if err != nil {
		fail(fmt.Sprintf("analysing %s: %v", localPath, err))
	}
	if res.Canonical.SHA256 == "" {
		fail(fmt.Sprintf("%s was not recognised as a %s cartridge image (%s); nothing was uploaded",
			localPath, platformSlug, lastNote(res.Notes)))
	}
	if res.Platform != protocol.PlatformID(platformSlug) {
		fail(fmt.Sprintf("%s looks like %s, not %s; pass the platform it actually is", localPath, res.Platform, platformSlug))
	}
	fmt.Printf("  canonical sha256: %s (%d bytes)\n", res.Canonical.SHA256, res.Canonical.Size)

	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 2*time.Minute)
	client, report := connect(probeCtx, baseURL, token)
	cancelProbe()

	platforms, err := romm.FetchPlatforms(context.Background(), client, report)
	if err != nil {
		fail(fmt.Sprintf("fetching RomM's platform list: %v", err))
	}
	if _, err := platforms.Lookup(platformSlug); err != nil {
		fail(err.Error())
	}

	stagingDir, err := os.MkdirTemp("", "swarm-bridge-staging-")
	if err != nil {
		fail(fmt.Sprintf("creating a staging directory: %v", err))
	}
	defer os.RemoveAll(stagingDir)
	staging, err := destination.NewStaging(stagingDir)
	if err != nil {
		fail(fmt.Sprintf("preparing staging: %v", err))
	}

	library := &ingest.RommLibrary{Source: &scan.RommSource{Client: client, Report: report}}
	flow := &ingest.Flow{
		Mode:       protocol.ModeAPIOnly,
		Staging:    staging,
		Journal:    ingest.NewMemoryJournal(),
		Uploader:   &ingest.RommUploader{Client: client, Report: report, Platforms: platforms},
		Reconciler: &ingest.Reconciler{Library: library, Timeout: *timeout},
	}

	f, err := os.Open(localPath)
	if err != nil {
		fail(fmt.Sprintf("opening %s: %v", localPath, err))
	}
	defer f.Close()

	expected := ingest.Expected{
		Platform:  res.Platform,
		FileID:    protocol.FileIDFromCanonicalDigest(res.Canonical.SHA256),
		Canonical: res.Canonical,
	}
	relativeDest := filepath.Join(platformSlug, filepath.Base(localPath))
	id := protocol.NewTransferID()

	fmt.Printf("Uploading through RomM's chunked upload API...\n")
	fmt.Printf("Waiting up to %s for RomM to index and match it — its own filesystem watcher has a\n"+
		"confirmed five-minute debounce, so this is normal, not stuck.\n", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+2*time.Minute)
	defer cancel()

	start := time.Now()
	state, recvErr := flow.Receive(ctx, id, f, expected, relativeDest)

	fmt.Println()
	for _, t := range flow.Journal.History(id) {
		fmt.Printf("  %-28s %s\n", t.To, t.Detail)
	}
	fmt.Printf("\nFinished in %s, final state: %s\n", time.Since(start).Round(time.Second), state)

	if recvErr != nil {
		fail(recvErr.Error())
	}
	if state != protocol.StateSourceActive {
		fmt.Fprintf(os.Stderr, "\nswarm-bridge: the transfer did not reach source_active — see the state above for why.\n")
		os.Exit(1)
	}
}

func lastNote(notes []string) string {
	if len(notes) == 0 {
		return "no further detail was recorded"
	}
	return notes[len(notes)-1]
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "swarm-bridge: "+msg)
	os.Exit(1)
}
