package hostapi_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hostapi"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func buildTestCatalogueDATForHostapi(gameName string, payload []byte) []byte {
	d := protocol.DigestBytes(payload)
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n<datafile>\n")
	b.WriteString("  <header>\n    <name>Nintendo - Game Boy</name>\n    <version>1</version>\n  </header>\n")
	fmt.Fprintf(&b, "  <game name=%q>\n    <rom name=\"%s.gb\" size=\"%d\" crc=\"%s\" md5=\"%s\" sha1=\"%s\"/>\n  </game>\n",
		gameName, gameName, d.Size, strings.ToUpper(d.CRC32), strings.ToUpper(d.MD5), strings.ToUpper(d.SHA1))
	b.WriteString("</datafile>\n")
	return []byte(b.String())
}

// TestPhase2_GetReferenceCataloguesOverHTTP is the wire-level proof for the
// new /reference-catalogues route: a catalogue the Host owner uploads
// (directly through Directory, standing in for the Host UI's own upload
// handler — see host/hostui, which calls the same Directory method) is
// what a Bridge actually receives back over HTTP, content included.
func TestPhase2_GetReferenceCataloguesOverHTTP(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	dir := directory.New(db)
	s := hostapi.New(dir)

	swarmID, bridgeID, _, refreshToken := enrollTestBridge(t, s)

	// Upload directly through the same Directory the hostapi.Server wraps —
	// this test only needs the catalogue to exist, not to re-prove the
	// upload path itself (see host/directory's own tests for that).
	// enrollTestBridge's setup created the owner via /api/setup under
	// username "owner"; look that account up directly rather than
	// threading it back out of enrollTestBridge just for this one test.
	var ownerID string
	if err := db.QueryRow(`SELECT id FROM accounts WHERE username = 'owner'`).Scan(&ownerID); err != nil {
		t.Fatalf("looking up the owner account: %v", err)
	}

	dat := buildTestCatalogueDATForHostapi("Wire Test Game", []byte("wire test payload"))
	if err := dir.UploadReferenceCatalogue(t.Context(), protocol.UserID(ownerID), protocol.SwarmID(swarmID), protocol.PlatformGB, "wire.dat", dat); err != nil {
		t.Fatalf("UploadReferenceCatalogue: %v", err)
	}

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/reference-catalogues", "", map[string]any{
		"refresh_token": refreshToken,
		"swarm_id":      swarmID,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("POST /api/bridges/{id}/reference-catalogues: status %d, body %s", resp.Code, resp.Body.String())
	}

	body := decodeBody(t, resp)
	catalogues, _ := body["catalogues"].([]any)
	if len(catalogues) != 1 {
		t.Fatalf("catalogues = %d entr(ies), want 1: %+v", len(catalogues), body)
	}
	entry, _ := catalogues[0].(map[string]any)
	if entry["platform"] != "gb" || entry["filename"] != "wire.dat" {
		t.Fatalf("catalogue entry = %+v, want platform gb, filename wire.dat", entry)
	}
	contentB64, _ := entry["content_base64"].(string)
	content, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		t.Fatalf("decoding content_base64: %v", err)
	}
	if string(content) != string(dat) {
		t.Fatal("returned content does not match what was uploaded")
	}
}

func TestPhase2_GetReferenceCataloguesOverHTTPRejectsABadToken(t *testing.T) {
	s := newTestServer(t)
	swarmID, bridgeID, _, _ := enrollTestBridge(t, s)

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/reference-catalogues", "", map[string]any{
		"refresh_token": "not-the-real-token",
		"swarm_id":      swarmID,
	})
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/bridges/{id}/reference-catalogues with a bad token: status %d, body %s", resp.Code, resp.Body.String())
	}
}

func TestPhase2_GetReferenceCataloguesOverHTTPReturnsEmptyWhenNoneUploaded(t *testing.T) {
	s := newTestServer(t)
	swarmID, bridgeID, _, refreshToken := enrollTestBridge(t, s)

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/reference-catalogues", "", map[string]any{
		"refresh_token": refreshToken,
		"swarm_id":      swarmID,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("POST /api/bridges/{id}/reference-catalogues: status %d, body %s", resp.Code, resp.Body.String())
	}
	body := decodeBody(t, resp)
	catalogues, _ := body["catalogues"].([]any)
	if len(catalogues) != 0 {
		t.Fatalf("catalogues = %d entr(ies), want 0 when none were ever uploaded", len(catalogues))
	}
}
