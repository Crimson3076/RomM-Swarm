package bridgeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func newStore(t *testing.T) *FileStore {
	t.Helper()
	dir := t.TempDir()
	return NewFileStore(filepath.Join(dir, "config.json"))
}

func TestPhase0_UnconfiguredStoreReadsAsNotConfigured(t *testing.T) {
	store := newStore(t)

	if _, err := store.Load(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Load on an empty store returned %v, want ErrNotConfigured", err)
	}

	var zero Config
	if zero.Configured() {
		t.Fatal("a zero-value Config reports itself as configured")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	store := newStore(t)

	c := Config{
		RommURL:           "https://romm.example",
		RommToken:         "rmm_test",
		DestinationMode:   protocol.ModeAPIOnly,
		ReceiveStagingDir: "/data/staging",
		AdminPasswordHash: "pbkdf2$...",
		ListenAddr:        ":8080",
	}
	if err := store.Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RommURL != c.RommURL || got.RommToken != c.RommToken || got.DestinationMode != c.DestinationMode ||
		got.ReceiveStagingDir != c.ReceiveStagingDir || got.AdminPasswordHash != c.AdminPasswordHash ||
		got.ListenAddr != c.ListenAddr {
		t.Fatalf("round trip changed the config: got %+v, want %+v", got, c)
	}
	if !got.Configured() {
		t.Fatal("a config with a URL and a token reports itself as unconfigured")
	}
	if got.SchemaVersion != protocol.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d (Save should stamp it)", got.SchemaVersion, protocol.SchemaVersion)
	}
	if got.PersistedAt.IsZero() {
		t.Error("PersistedAt was not stamped")
	}
}

func TestSaveRejectsAnUnsupportedDestinationMode(t *testing.T) {
	store := newStore(t)
	err := store.Save(Config{DestinationMode: protocol.DestinationMode("not_a_real_mode")})
	if err == nil {
		t.Fatal("Save accepted an unsupported destination mode")
	}
}

func TestCorruptConfigFileFailsClearlyRatherThanSilently(t *testing.T) {
	store := newStore(t)
	if err := os.WriteFile(store.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing a corrupt config file: %v", err)
	}
	_, err := store.Load()
	if err == nil {
		t.Fatal("Load accepted a corrupt config file")
	}
	if errors.Is(err, ErrNotConfigured) {
		t.Fatal("a corrupt config file was reported as simply unconfigured, hiding the real problem")
	}
}

// TestPhase0_SaveSurvivesACrashAtEveryStep mirrors
// auth.TestPhase0_RefreshRotationSurvivesACrashAtEveryStep: a crash at any
// point during Save must never leave the store readable as a torn write —
// either the old config (or none) or the fully-written new one, never a
// mixture.
func TestPhase0_SaveSurvivesACrashAtEveryStep(t *testing.T) {
	steps := []string{"write", "fsync", "rename"}

	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			store := newStore(t)

			original := Config{RommURL: "https://before.example", RommToken: "before-token"}
			if err := store.Save(original); err != nil {
				t.Fatalf("seeding the original config: %v", err)
			}

			store.crashAfter = step
			err := store.Save(Config{RommURL: "https://after.example", RommToken: "after-token"})
			if !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("expected a simulated crash, got %v", err)
			}
			store.crashAfter = ""

			got, loadErr := store.Load()
			if loadErr != nil {
				t.Fatalf("the store was unreadable after a simulated crash at %q: %v", step, loadErr)
			}

			// A crash after rename is a completed write from the filesystem's
			// point of view (the directory fsync missing is a durability
			// concern under real power loss, not something a plain reload
			// in the same test process can observe) — every other step must
			// leave the original config untouched.
			if step == "rename" {
				if got.RommURL != "https://after.example" {
					t.Fatalf("after a crash post-rename, got %q, want the new config", got.RommURL)
				}
			} else if got.RommURL != original.RommURL {
				t.Fatalf("a crash at %q corrupted the previous config: got %q, want %q", step, got.RommURL, original.RommURL)
			}

			// No leftover temp files, whichever step crashed.
			entries, err := os.ReadDir(filepath.Dir(store.Path))
			if err != nil {
				t.Fatalf("reading the config directory: %v", err)
			}
			for _, e := range entries {
				if filepath.Ext(e.Name()) == ".tmp" {
					t.Errorf("a temporary file was left behind after a crash at %q: %s", step, e.Name())
				}
			}
		})
	}
}

func TestConfigFileIsNotWorldReadable(t *testing.T) {
	store := newStore(t)
	if err := store.Save(Config{RommURL: "https://example", RommToken: "secret-token"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("config file permissions are %v, want no access for group/other since it holds a RomM token", info.Mode().Perm())
	}
}
