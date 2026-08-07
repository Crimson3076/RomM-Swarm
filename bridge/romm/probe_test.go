package romm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/internal/fakeromm"
)

const testToken = "rmm_test_credential_value"

func probeAgainst(t *testing.T, srv *fakeromm.Server, opts ProbeOptions) *Report {
	t.Helper()
	opts.BaseURL = srv.URL
	rep, err := Probe(context.Background(), opts)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return rep
}

// TestPhase0_ProbeConfirmsRequiredCapabilities is the Phase 0 deliverable
// "Confirm supported RomM versions and relevant API endpoints", exercised
// end to end.
func TestPhase0_ProbeConfirmsRequiredCapabilities(t *testing.T) {
	srv := fakeromm.New(fakeromm.Options{Token: testToken, Version: "5.0.0"})
	defer srv.Close()

	rep := probeAgainst(t, srv, ProbeOptions{Token: testToken})

	if len(rep.Blockers) != 0 {
		t.Fatalf("a fully capable server produced blockers: %v", rep.Blockers)
	}
	if rep.ServerVersion != "5.0.0" {
		t.Errorf("server version = %q, want 5.0.0", rep.ServerVersion)
	}
	if rep.AuthScheme != "bearer" {
		t.Errorf("auth scheme = %q, want bearer", rep.AuthScheme)
	}
	if rep.SpecTitle == "" || rep.SpecVersion == "" {
		t.Error("the report does not identify the specification it read")
	}

	for _, c := range rep.Capabilities {
		if c.Required && !c.Found {
			t.Errorf("required capability %s was not found", c.ID)
		}
		if c.Needs == "" {
			t.Errorf("capability %s does not say which Phase 0 deliverable needs it", c.ID)
		}
	}

	// Phase 0: "Confirm available hashes, metadata identifiers, file sizes, and
	// platform identifiers."
	if len(rep.HashFields) == 0 {
		t.Fatal("no hash fields were found on a ROM object")
	}
	shape := map[string]string{}
	for _, f := range rep.ROMFields {
		shape[f.Name] = f.Type
	}
	for _, want := range []string{"platform_id", "fs_size_bytes", "fs_name"} {
		if _, ok := shape[want]; !ok {
			t.Errorf("the ROM object shape has no %q field; observed: %v", want, shape)
		}
	}
}

// TestPhase0_MissingRequiredCapabilityIsABlocker is the go/stop rule doing its
// job: a server that cannot serve content is reported as such, loudly.
func TestPhase0_MissingRequiredCapabilityIsABlocker(t *testing.T) {
	srv := fakeromm.New(fakeromm.Options{
		Token:     testToken,
		OmitPaths: []string{"/api/roms/{id}/files/content/{file_name}"},
	})
	defer srv.Close()

	rep := probeAgainst(t, srv, ProbeOptions{Token: testToken})

	if len(rep.Blockers) == 0 {
		t.Fatal("a server with no download endpoint produced no blockers")
	}
	found := false
	for _, c := range rep.Capabilities {
		if c.ID == "roms.download" {
			found = c.Found
			if len(c.Tried) == 0 {
				t.Error("the missing capability does not record what was looked for")
			}
		}
	}
	if found {
		t.Error("roms.download was reported as found on a server that omits it")
	}

	// A blocker must be actionable: it names the capability and points at the
	// inventory of paths the server does document.
	joined := strings.Join(rep.Blockers, " ")
	if !strings.Contains(joined, "roms.download") {
		t.Errorf("the blocker does not name the missing capability: %v", rep.Blockers)
	}
	if len(rep.PathInventory) == 0 {
		t.Error("the report carries no path inventory, so a mismatch cannot be corrected")
	}
}

// Current RomM uses a three-step chunked upload. Finding only the start route
// is not enough: the Bridge must be able to send chunks and atomically complete
// the session before API-only publication is viable.
func TestPhase0_EveryChunkedUploadStageIsRequired(t *testing.T) {
	cases := []struct {
		path string
		id   string
	}{
		{"/api/roms/upload/start", "roms.upload.start"},
		{"/api/roms/upload/{upload_id}", "roms.upload.chunk"},
		{"/api/roms/upload/{upload_id}/complete", "roms.upload.complete"},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			srv := fakeromm.New(fakeromm.Options{Token: testToken, OmitPaths: []string{tc.path}})
			defer srv.Close()

			rep := probeAgainst(t, srv, ProbeOptions{Token: testToken})
			if !strings.Contains(strings.Join(rep.Blockers, " "), tc.id) {
				t.Fatalf("missing %s did not produce a blocker: %v", tc.id, rep.Blockers)
			}
		})
	}
}

// The probe must find the credential presentation rather than assume one.
func TestProbeDetectsAlternativeCredentialPresentations(t *testing.T) {
	for _, scheme := range []string{"bearer", "x-api-key", "authorization-raw"} {
		t.Run(scheme, func(t *testing.T) {
			srv := fakeromm.New(fakeromm.Options{Token: testToken, AuthScheme: scheme})
			defer srv.Close()

			rep := probeAgainst(t, srv, ProbeOptions{Token: testToken})
			if rep.AuthScheme != scheme {
				t.Fatalf("auth scheme = %q, want %q", rep.AuthScheme, scheme)
			}
			if len(rep.Blockers) != 0 {
				t.Errorf("blockers on a working server: %v", rep.Blockers)
			}
		})
	}
}

func TestRejectedCredentialIsABlockerNotACrash(t *testing.T) {
	srv := fakeromm.New(fakeromm.Options{Token: testToken, RejectAllCredentials: true})
	defer srv.Close()

	rep := probeAgainst(t, srv, ProbeOptions{Token: "wrong-token"})

	if len(rep.Blockers) == 0 {
		t.Fatal("a rejected credential produced no blockers")
	}
	// The report must still carry the specification findings: a credential
	// problem must not hide what the server can do.
	if rep.SpecTitle == "" || len(rep.Capabilities) == 0 {
		t.Error("the report lost its specification findings when authentication failed")
	}
}

// TestPhase0_ProbeOutputNeverContainsTheCredential is Scope of Work §7: no
// credential logging, and the fixture bundle is committed.
func TestPhase0_ProbeOutputNeverContainsTheCredential(t *testing.T) {
	srv := fakeromm.New(fakeromm.Options{Token: testToken, RejectAllCredentials: true})
	defer srv.Close()

	// Force every error path: a wrong credential against a server that refuses
	// everything, so failures are recorded in findings and blockers.
	rep := probeAgainst(t, srv, ProbeOptions{Token: testToken})

	encoded, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("encoding the report: %v", err)
	}
	if strings.Contains(string(encoded), testToken) {
		t.Fatal("the probe report contains the credential")
	}

	// The host is redacted unless the operator opts in.
	if rep.Host != "[redacted]" {
		t.Errorf("host = %q, want it redacted by default", rep.Host)
	}
	withHost := probeAgainst(t, srv, ProbeOptions{Token: testToken, IncludeHost: true})
	if withHost.Host == "[redacted]" {
		t.Error("IncludeHost did not record the host")
	}
}

// A library's contents must not end up in a committed fixture by accident.
func TestSamplesAreOmittedUnlessRequested(t *testing.T) {
	srv := fakeromm.New(fakeromm.Options{Token: testToken})
	defer srv.Close()

	if rep := probeAgainst(t, srv, ProbeOptions{Token: testToken}); rep.Sample != nil {
		t.Fatal("a sample library object was recorded without being asked for")
	}

	rep := probeAgainst(t, srv, ProbeOptions{Token: testToken, IncludeSamples: true})
	if rep.Sample == nil {
		t.Fatal("IncludeSamples did not record a sample")
	}

	// Even then, the shape record must carry names and types only.
	for _, f := range rep.ROMFields {
		if f.Type == "" {
			t.Errorf("field %q has no recorded type", f.Name)
		}
	}
}

func TestServerWithoutHashesIsABlocker(t *testing.T) {
	srv := fakeromm.New(fakeromm.Options{Token: testToken, NoHashes: true})
	defer srv.Close()

	rep := probeAgainst(t, srv, ProbeOptions{Token: testToken})
	if len(rep.HashFields) != 0 {
		t.Fatalf("hash fields were reported on a server that has none: %v", rep.HashFields)
	}
	joined := strings.Join(rep.Blockers, " ")
	if !strings.Contains(joined, "hash") {
		t.Errorf("a server with no content hashes did not produce a hash blocker: %v", rep.Blockers)
	}
}

func TestUnreachableSpecProducesAReportNotAnError(t *testing.T) {
	srv := fakeromm.New(fakeromm.Options{Token: testToken, SpecPath: "/somewhere/else.json"})
	defer srv.Close()

	rep := probeAgainst(t, srv, ProbeOptions{Token: testToken})
	if len(rep.Blockers) == 0 {
		t.Fatal("a server publishing no specification produced no blockers")
	}
	if rep.ProbedAt.IsZero() {
		t.Error("the report was not stamped")
	}
}

func TestProbeRequiresAServerURL(t *testing.T) {
	if _, err := Probe(context.Background(), ProbeOptions{}); err == nil {
		t.Fatal("Probe accepted an empty server URL")
	}
}

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/api/roms/{id}":                    "/api/roms/*",
		"/api/roms/{rom_id}/content/{file}": "/api/roms/*/content/*",
		"/api/platforms":                    "/api/platforms",
	}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}

	// Two servers naming the same parameter differently must match the same
	// capability.
	if NormalizePath("/api/roms/{id}") != NormalizePath("/api/roms/{rom_id}") {
		t.Error("differently named path parameters did not normalise to the same shape")
	}
}

func TestRedactionCoversEveryOccurrence(t *testing.T) {
	c := NewClient("https://example.invalid", testToken, AuthScheme{})
	in := "GET https://example.invalid/api?token=" + testToken + " failed for " + testToken
	got := c.Redact(in)
	if strings.Contains(got, testToken) {
		t.Fatalf("redaction left the credential in place: %q", got)
	}
	if strings.Count(got, "[redacted]") != 2 {
		t.Errorf("expected two redactions, got %q", got)
	}
}

func TestIsHashField(t *testing.T) {
	for _, name := range []string{"crc_hash", "md5_hash", "sha1_hash", "sha256_hash", "crc", "md5", "sha1", "file_md5"} {
		if !isHashField(name) {
			t.Errorf("%q was not recognised as a hash field", name)
		}
	}
	for _, name := range []string{"name", "platform_id", "summary", "hash_algorithm"} {
		if isHashField(name) {
			t.Errorf("%q was wrongly recognised as a hash field", name)
		}
	}
}
