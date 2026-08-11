package main

import (
	"context"
	"fmt"

	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
)

// Connection is a live, ready-to-use RomM connection: the client, its probe
// report, and the resolved platform list. Nearly everything downstream
// (listing, upload, reconciliation) needs all three, so they travel
// together rather than being re-derived at each call site.
type Connection struct {
	Client    *romm.Client
	Report    *romm.Report
	Platforms *romm.Platforms
}

// connect probes baseURL/token and resolves the platform list in one step.
func connect(ctx context.Context, baseURL, token string) (*Connection, error) {
	client, report, err := romm.Connect(ctx, baseURL, token)
	if err != nil {
		return nil, err
	}
	platforms, err := romm.FetchPlatforms(ctx, client, report)
	if err != nil {
		return nil, fmt.Errorf("fetching RomM's platform list: %w", err)
	}
	return &Connection{Client: client, Report: report, Platforms: platforms}, nil
}
