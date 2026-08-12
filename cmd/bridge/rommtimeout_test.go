package main

import (
	"context"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
)

func TestRommHTTPTimeoutFromEnvDefaultsWhenUnset(t *testing.T) {
	if got := rommHTTPTimeoutFromEnv(); got != DefaultRommHTTPTimeout {
		t.Fatalf("rommHTTPTimeoutFromEnv() = %v, want the default %v", got, DefaultRommHTTPTimeout)
	}
}

func TestRommHTTPTimeoutFromEnvUsesACustomValue(t *testing.T) {
	t.Setenv("BRIDGE_ROMM_HTTP_TIMEOUT_SECONDS", "180")
	if got := rommHTTPTimeoutFromEnv(); got != 180*time.Second {
		t.Fatalf("rommHTTPTimeoutFromEnv() = %v, want 180s", got)
	}
}

func TestRommHTTPTimeoutFromEnvFallsBackOnAnInvalidValue(t *testing.T) {
	t.Setenv("BRIDGE_ROMM_HTTP_TIMEOUT_SECONDS", "not-a-number")
	if got := rommHTTPTimeoutFromEnv(); got != DefaultRommHTTPTimeout {
		t.Fatalf("rommHTTPTimeoutFromEnv() = %v, want the default on a non-numeric value", got)
	}

	t.Setenv("BRIDGE_ROMM_HTTP_TIMEOUT_SECONDS", "0")
	if got := rommHTTPTimeoutFromEnv(); got != DefaultRommHTTPTimeout {
		t.Fatalf("rommHTTPTimeoutFromEnv() = %v, want the default on a non-positive value", got)
	}
}

// TestReconnectAppliesTheConfiguredRommHTTPTimeout is the Daemon-level
// proof for the operator report this fixes: a "context deadline exceeded"
// while scanning a RomM server not on the same local network. Reconnect
// must apply BRIDGE_ROMM_HTTP_TIMEOUT_SECONDS to the live connection's
// HTTP client, not just leave bridge/romm's own tighter same-LAN default
// in place.
func TestReconnectAppliesTheConfiguredRommHTTPTimeout(t *testing.T) {
	t.Setenv("BRIDGE_ROMM_HTTP_TIMEOUT_SECONDS", "180")

	fb := newFakeBridgeServer()
	srv := fb.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: srv.URL, RommToken: "t"}); err != nil {
		t.Fatalf("saving the RomM config: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	conn := d.Connection()
	if conn == nil {
		t.Fatal("Connection() = nil after a successful Reconnect")
	}
	if conn.Client.HTTP.Timeout != 180*time.Second {
		t.Fatalf("the live connection's HTTP timeout = %v, want 180s", conn.Client.HTTP.Timeout)
	}
}
