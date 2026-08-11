package romm

import (
	"context"
	"fmt"
)

// Connection is a live, ready-to-use RomM connection: the client, its probe
// report, and the resolved platform list. Nearly everything past the
// initial probe needs all three, so they travel together rather than being
// re-derived at each call site.
type Connection struct {
	Client    *Client
	Report    *Report
	Platforms *Platforms
}

// ConnectAndResolvePlatforms probes baseURL/token and resolves the platform
// list in one step — Connect plus FetchPlatforms.
func ConnectAndResolvePlatforms(ctx context.Context, baseURL, token string) (*Connection, error) {
	client, report, err := Connect(ctx, baseURL, token)
	if err != nil {
		return nil, err
	}
	platforms, err := FetchPlatforms(ctx, client, report)
	if err != nil {
		return nil, fmt.Errorf("fetching RomM's platform list: %w", err)
	}
	return &Connection{Client: client, Report: report, Platforms: platforms}, nil
}
