package directory_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// buildTestCatalogueDAT renders a minimal, real Logiqx catalogue — one
// game, one ROM, with hashes computed from payload — matching the pattern
// bridge/scan's and cmd/bridge's own tests already use for DAT fixtures.
func buildTestCatalogueDAT(gameName string, payload []byte) []byte {
	d := protocol.DigestBytes(payload)
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n<datafile>\n")
	b.WriteString("  <header>\n    <name>Nintendo - Game Boy</name>\n    <version>1</version>\n  </header>\n")
	fmt.Fprintf(&b, "  <game name=%q>\n    <rom name=\"%s.gb\" size=\"%d\" crc=\"%s\" md5=\"%s\" sha1=\"%s\"/>\n  </game>\n",
		gameName, gameName, d.Size, strings.ToUpper(d.CRC32), strings.ToUpper(d.MD5), strings.ToUpper(d.SHA1))
	b.WriteString("</datafile>\n")
	return []byte(b.String())
}

func TestPhase2_UploadReferenceCatalogueRejectsAnUnsupportedPlatform(t *testing.T) {
	d, owner, swarmID, _, _, _ := enrolledFixture(t)
	ctx := context.Background()

	dat := buildTestCatalogueDAT("Test Game", []byte("payload"))
	err := d.UploadReferenceCatalogue(ctx, owner, swarmID, protocol.PlatformID("n64"), "test.dat", dat)
	if err != directory.ErrUnsupportedPlatform {
		t.Fatalf("UploadReferenceCatalogue for an unsupported platform: err = %v, want ErrUnsupportedPlatform", err)
	}
}

func TestPhase2_UploadReferenceCatalogueRejectsAFileThatDoesNotParse(t *testing.T) {
	d, owner, swarmID, _, _, _ := enrolledFixture(t)
	ctx := context.Background()

	err := d.UploadReferenceCatalogue(ctx, owner, swarmID, protocol.PlatformGB, "bad.dat", []byte("not xml at all"))
	if err == nil {
		t.Fatal("UploadReferenceCatalogue with unparseable content succeeded, want an error")
	}
}

func TestPhase2_UploadThenListThenGetReferenceCatalogueRoundTrips(t *testing.T) {
	d, owner, swarmID, bridgeID, _, token := enrolledFixture(t)
	ctx := context.Background()

	payload := []byte("a fake game boy rom payload")
	dat := buildTestCatalogueDAT("Test Game (USA)", payload)

	if err := d.UploadReferenceCatalogue(ctx, owner, swarmID, protocol.PlatformGB, "gb.dat", dat); err != nil {
		t.Fatalf("UploadReferenceCatalogue: %v", err)
	}

	listed, err := d.ListReferenceCatalogues(ctx, owner, swarmID)
	if err != nil {
		t.Fatalf("ListReferenceCatalogues: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListReferenceCatalogues returned %d entr(ies), want 1", len(listed))
	}
	if listed[0].Platform != protocol.PlatformGB || listed[0].Filename != "gb.dat" || listed[0].EntryCount != 1 {
		t.Fatalf("listed catalogue = %+v, want platform gb, filename gb.dat, 1 entry", listed[0])
	}
	if listed[0].Content != nil {
		t.Fatalf("ListReferenceCatalogues returned Content, want nil (metadata only)")
	}

	fetched, err := d.GetReferenceCatalogues(ctx, bridgeID, token, swarmID)
	if err != nil {
		t.Fatalf("GetReferenceCatalogues: %v", err)
	}
	if len(fetched) != 1 {
		t.Fatalf("GetReferenceCatalogues returned %d entr(ies), want 1", len(fetched))
	}
	if string(fetched[0].Content) != string(dat) {
		t.Fatalf("fetched content does not match what was uploaded")
	}
}

// TestPhase2_UploadReferenceCatalogueReplacesNotAccumulates proves there is
// no upload history — a second upload for the same platform overwrites the
// first outright (ADR 0023: the Host is the sole current authority per
// platform, not a version log).
func TestPhase2_UploadReferenceCatalogueReplacesNotAccumulates(t *testing.T) {
	d, owner, swarmID, _, _, _ := enrolledFixture(t)
	ctx := context.Background()

	first := buildTestCatalogueDAT("First Game", []byte("first payload"))
	if err := d.UploadReferenceCatalogue(ctx, owner, swarmID, protocol.PlatformGB, "first.dat", first); err != nil {
		t.Fatalf("UploadReferenceCatalogue (first): %v", err)
	}
	second := buildTestCatalogueDAT("Second Game", []byte("second payload"))
	if err := d.UploadReferenceCatalogue(ctx, owner, swarmID, protocol.PlatformGB, "second.dat", second); err != nil {
		t.Fatalf("UploadReferenceCatalogue (second): %v", err)
	}

	listed, err := d.ListReferenceCatalogues(ctx, owner, swarmID)
	if err != nil {
		t.Fatalf("ListReferenceCatalogues: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListReferenceCatalogues returned %d entr(ies), want exactly 1 (replaced, not accumulated)", len(listed))
	}
	if listed[0].Filename != "second.dat" {
		t.Fatalf("listed filename = %q, want %q (the most recent upload)", listed[0].Filename, "second.dat")
	}
}

func TestPhase2_DeleteReferenceCatalogueRemovesIt(t *testing.T) {
	d, owner, swarmID, _, _, _ := enrolledFixture(t)
	ctx := context.Background()

	dat := buildTestCatalogueDAT("Test Game", []byte("payload"))
	if err := d.UploadReferenceCatalogue(ctx, owner, swarmID, protocol.PlatformGB, "gb.dat", dat); err != nil {
		t.Fatalf("UploadReferenceCatalogue: %v", err)
	}
	if err := d.DeleteReferenceCatalogue(ctx, owner, swarmID, protocol.PlatformGB); err != nil {
		t.Fatalf("DeleteReferenceCatalogue: %v", err)
	}

	listed, err := d.ListReferenceCatalogues(ctx, owner, swarmID)
	if err != nil {
		t.Fatalf("ListReferenceCatalogues: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("ListReferenceCatalogues after delete returned %d entr(ies), want 0", len(listed))
	}

	// Deleting something already absent is not an error.
	if err := d.DeleteReferenceCatalogue(ctx, owner, swarmID, protocol.PlatformGB); err != nil {
		t.Fatalf("DeleteReferenceCatalogue on an already-empty platform: %v", err)
	}
}

func TestPhase2_GetReferenceCataloguesRejectsABadToken(t *testing.T) {
	d, _, swarmID, bridgeID, _, _ := enrolledFixture(t)
	ctx := context.Background()

	if _, err := d.GetReferenceCatalogues(ctx, bridgeID, "not-the-real-token", swarmID); err != auth.ErrUnknownToken {
		t.Fatalf("GetReferenceCatalogues with a bad token: err = %v, want auth.ErrUnknownToken", err)
	}
}

func TestPhase2_GetReferenceCataloguesRejectsABridgeNotEnrolledInTheNamedSwarm(t *testing.T) {
	d, _, _, bridgeID, _, token := enrolledFixture(t)
	ctx := context.Background()

	if _, err := d.GetReferenceCatalogues(ctx, bridgeID, token, protocol.NewSwarmID()); err != directory.ErrBridgeNotEnrolledInSwarm {
		t.Fatalf("GetReferenceCatalogues against a Swarm never joined: err = %v, want ErrBridgeNotEnrolledInSwarm", err)
	}
}
