package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultRommHTTPTimeout bounds every HTTP request this Bridge makes to
// its own RomM server — both inventory-listing calls and full ROM
// downloads share this one http.Client-level timeout
// (bridge/romm.Client.HTTP.Timeout). bridge/romm's own hard-coded 30
// second default is tuned for a same-LAN connection; a Bridge and its RomM
// server on opposite ends of a slower or more distant network link
// legitimately need longer — especially for a large Nintendo DS dump (up
// to 512MiB, ADR 0004) mid-scan. Raised here after an operator report of
// exactly that: "romm: reading /api/roms: context deadline exceeded" on a
// Bridge not on the same local network as its RomM server.
const DefaultRommHTTPTimeout = 2 * time.Minute

// rommHTTPTimeoutFromEnv resolves BRIDGE_ROMM_HTTP_TIMEOUT_SECONDS,
// falling back to DefaultRommHTTPTimeout for an unset, non-numeric, or
// non-positive value — the same fallback shape as publishIntervalFromEnv.
func rommHTTPTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("BRIDGE_ROMM_HTTP_TIMEOUT_SECONDS"))
	if raw == "" {
		return DefaultRommHTTPTimeout
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return DefaultRommHTTPTimeout
	}
	return time.Duration(secs) * time.Second
}
