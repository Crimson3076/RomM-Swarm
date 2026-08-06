// Command swarm-probe records what a RomM server can do.
//
// It is the Phase 0 evidence tool. Point it at a RomM instance with a scoped
// standard-user Client API Token and it writes a fixture bundle describing that
// server's capabilities, which can then be committed and replayed in CI without
// the server.
//
// The credential is read from the environment, never from a flag, so it does not
// appear in shell history or in a process listing. It never appears in the
// output.
//
//	ROMM_URL=https://romm.example ROMM_TOKEN=... swarm-probe -out probe-out
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
)

func main() {
	out := flag.String("out", "probe-out", "directory to write the fixture bundle into")
	name := flag.String("name", "", "name for this bundle, defaulting to the reported server version")
	includeHost := flag.String("include-host", "no", "record the server address in the bundle (yes/no)")
	includeSamples := flag.String("include-samples", "no", "record one real library object in the bundle (yes/no)")
	timeout := flag.Duration("timeout", 2*time.Minute, "overall probe timeout")
	flag.Parse()

	baseURL := os.Getenv("ROMM_URL")
	token := os.Getenv("ROMM_TOKEN")

	if strings.TrimSpace(baseURL) == "" {
		fail("ROMM_URL is not set. Point it at your RomM server, for example https://romm.example")
	}
	if strings.TrimSpace(token) == "" {
		fmt.Fprintln(os.Stderr,
			"warning: ROMM_TOKEN is not set. Only the published specification will be examined;\n"+
				"         standard-user token behaviour cannot be verified without a credential.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	report, err := romm.Probe(ctx, romm.ProbeOptions{
		BaseURL:        baseURL,
		Token:          token,
		IncludeHost:    yes(*includeHost),
		IncludeSamples: yes(*includeSamples),
	})
	if err != nil {
		fail(err.Error())
	}

	bundleName := *name
	if bundleName == "" {
		bundleName = report.ServerVersion
	}
	if bundleName == "" {
		bundleName = "unknown-version"
	}
	bundleName = sanitize(bundleName)

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(fmt.Sprintf("could not create %s: %v", *out, err))
	}
	path := filepath.Join(*out, "romm-"+bundleName+".json")

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail(fmt.Sprintf("could not encode the report: %v", err))
	}
	// A last-resort check. The client redacts at its boundary, but this bundle is
	// meant to be committed, so it is worth refusing to write rather than
	// trusting that every path was covered.
	if token != "" && strings.Contains(string(encoded), token) {
		fail("the report unexpectedly contains the credential; refusing to write it")
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		fail(fmt.Sprintf("could not write %s: %v", path, err))
	}

	summarize(report, path)

	if len(report.Blockers) > 0 {
		// A non-zero exit makes this usable as a gate in a script.
		os.Exit(2)
	}
}

func summarize(r *romm.Report, path string) {
	fmt.Printf("RomM capability probe\n")
	fmt.Printf("  specification : %s %s (%s)\n", r.SpecTitle, r.SpecVersion, r.SpecPath)
	if r.ServerVersion != "" {
		fmt.Printf("  server version: %s\n", r.ServerVersion)
	}
	if r.AuthScheme != "" {
		fmt.Printf("  credential    : accepted as %s\n", r.AuthScheme)
	}
	if len(r.HashFields) > 0 {
		fmt.Printf("  hash fields   : %s\n", strings.Join(r.HashFields, ", "))
	}

	fmt.Printf("\nCapabilities\n")
	for _, c := range r.Capabilities {
		mark := "missing"
		if c.Found {
			mark = c.Method + " " + c.Path
		}
		required := " "
		if c.Required {
			required = "*"
		}
		fmt.Printf("  %s %-18s %s\n", required, c.ID, mark)
	}

	if len(r.Findings) > 0 {
		fmt.Printf("\nFindings\n")
		for _, f := range r.Findings {
			fmt.Printf("  - %s\n", f)
		}
	}
	if len(r.Blockers) > 0 {
		fmt.Printf("\nBlockers (Phase 0 go/stop)\n")
		for _, b := range r.Blockers {
			fmt.Printf("  ! %s\n", b)
		}
	}
	fmt.Printf("\nBundle written to %s\n", path)
	fmt.Printf("Review it before committing, then add it to docs/phase0/ as recorded evidence.\n")
}

func yes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "true", "1", "y":
		return true
	}
	return false
}

// sanitize keeps a filename to characters that are safe everywhere.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "swarm-probe: "+msg)
	os.Exit(1)
}
