package swarmconn

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func newStore(t *testing.T) *FileStore {
	t.Helper()
	dir := t.TempDir()
	return NewFileStore(filepath.Join(dir, "swarm-connection.json"))
}

func newTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a test identity key: %v", err)
	}
	return priv
}

func TestPhase0_UnjoinedStoreReadsAsNotJoined(t *testing.T) {
	store := newStore(t)

	if _, err := store.Load(); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("Load on an empty store returned %v, want ErrNotJoined", err)
	}

	var zero Config
	if zero.Configured() {
		t.Fatal("a zero-value Config reports itself as configured")
	}
	if zero.HasIdentity() {
		t.Fatal("a zero-value Config reports having an identity key")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	store := newStore(t)
	priv := newTestKey(t)

	c := Config{HostURL: "https://host.example", IdentityPrivateKey: priv}
	if err := store.Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.HostURL != c.HostURL {
		t.Fatalf("round trip changed HostURL: got %q, want %q", got.HostURL, c.HostURL)
	}
	if string(got.IdentityPrivateKey) != string(priv) {
		t.Fatal("round trip changed the identity private key")
	}
	if !got.Configured() {
		t.Fatal("a config with a Host URL and identity key reports itself as unconfigured")
	}
	if got.SchemaVersion != protocol.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d (Save should stamp it)", got.SchemaVersion, protocol.SchemaVersion)
	}
	if got.PersistedAt.IsZero() {
		t.Error("PersistedAt was not stamped")
	}
}

func TestBridgeIDIsDerivedFromTheStoredKeyNotStoredRedundantly(t *testing.T) {
	priv := newTestKey(t)
	c := Config{HostURL: "https://host.example", IdentityPrivateKey: priv}

	want := protocol.BridgeIDFromPublicKey(priv.Public().(ed25519.PublicKey))
	if got := c.BridgeID(); got != want {
		t.Fatalf("BridgeID() = %s, want %s", got, want)
	}
}

func TestValidateRejectsAHostURLWithNoIdentityKey(t *testing.T) {
	store := newStore(t)
	err := store.Save(Config{HostURL: "https://host.example"})
	if err == nil {
		t.Fatal("Save accepted a Host URL with no identity key")
	}
}

func TestValidateRejectsAMalformedIdentityKey(t *testing.T) {
	store := newStore(t)
	err := store.Save(Config{HostURL: "https://host.example", IdentityPrivateKey: []byte("too-short")})
	if err == nil {
		t.Fatal("Save accepted an identity key of the wrong length")
	}
}

func TestAnIdentityKeyAloneWithNoHostURLIsValid(t *testing.T) {
	// JoinSwarm generates the key before it has enrolled, so there is a
	// moment where an identity exists without a completed connection.
	store := newStore(t)
	priv := newTestKey(t)
	if err := store.Save(Config{IdentityPrivateKey: priv}); err != nil {
		t.Fatalf("Save rejected an identity-only config: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Configured() {
		t.Fatal("a config with no Host URL reports itself as configured")
	}
	if !got.HasIdentity() {
		t.Fatal("HasIdentity is false despite a stored key")
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
	if errors.Is(err, ErrNotJoined) {
		t.Fatal("a corrupt config file was reported as simply not joined, hiding the real problem")
	}
}

// TestPhase0_SaveSurvivesACrashAtEveryStep mirrors
// bridgeconfig.TestPhase0_SaveSurvivesACrashAtEveryStep: a crash at any
// point during Save must never leave the store readable as a torn write.
func TestPhase0_SaveSurvivesACrashAtEveryStep(t *testing.T) {
	steps := []string{"write", "fsync", "rename"}

	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			store := newStore(t)
			beforeKey := newTestKey(t)
			afterKey := newTestKey(t)

			original := Config{HostURL: "https://before.example", IdentityPrivateKey: beforeKey}
			if err := store.Save(original); err != nil {
				t.Fatalf("seeding the original config: %v", err)
			}

			store.crashAfter = step
			err := store.Save(Config{HostURL: "https://after.example", IdentityPrivateKey: afterKey})
			if !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("expected a simulated crash, got %v", err)
			}
			store.crashAfter = ""

			got, loadErr := store.Load()
			if loadErr != nil {
				t.Fatalf("the store was unreadable after a simulated crash at %q: %v", step, loadErr)
			}

			if step == "rename" {
				if got.HostURL != "https://after.example" {
					t.Fatalf("after a crash post-rename, got %q, want the new config", got.HostURL)
				}
			} else if got.HostURL != original.HostURL {
				t.Fatalf("a crash at %q corrupted the previous config: got %q, want %q", step, got.HostURL, original.HostURL)
			}

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
	if err := store.Save(Config{HostURL: "https://host.example", IdentityPrivateKey: newTestKey(t)}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("config file permissions are %v, want no access for group/other since it holds a private key", info.Mode().Perm())
	}
}
