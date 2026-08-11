package romm

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Connect probes baseURL and returns a ready-to-use client and report, or an
// error explaining exactly what went wrong.
//
// This is the sequence every tool in this repository follows: probe first,
// then build every subsequent request from what the probe actually
// resolved. It was originally a CLI-shaped private helper in
// cmd/swarm-bridge/main.go (os.Exit on any problem); moved here, unchanged
// in sequence, so a long-running process (cmd/bridge, bridge/adminui) can
// handle a connection failure without exiting.
func Connect(ctx context.Context, baseURL, token string) (*Client, *Report, error) {
	report, err := Probe(ctx, ProbeOptions{BaseURL: baseURL, Token: token})
	if err != nil {
		// Probe only returns an error for a malformed call (e.g. no URL at
		// all); every network or server-side failure instead lands in
		// report.Blockers/Findings below, deliberately, so a partial probe
		// is still informative — see Probe's own doc comment.
		return nil, nil, fmt.Errorf("probing %s: %w", baseURL, err)
	}
	if len(report.Blockers) > 0 {
		return nil, nil, fmt.Errorf("could not reach %s: %s", baseURL, strings.Join(report.Blockers, "; "))
	}
	if missing := MissingRequired(report.Capabilities); len(missing) > 0 {
		ids := make([]string, len(missing))
		for i, c := range missing {
			ids[i] = c.ID
		}
		return nil, nil, fmt.Errorf("the server is missing required capabilities: %s", strings.Join(ids, ", "))
	}
	if report.AuthScheme == "" {
		return nil, nil, errors.New("the server did not accept the credential — check the RomM token")
	}
	scheme, ok := LookupAuthScheme(report.AuthScheme)
	if !ok {
		return nil, nil, fmt.Errorf("the probe accepted the credential under scheme %q, which this build does not recognise", report.AuthScheme)
	}
	return NewClient(baseURL, token, scheme), report, nil
}
