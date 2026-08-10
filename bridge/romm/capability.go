package romm

import (
	"sort"
	"strings"
)

// The capability requirement table.
//
// This is the honest centre of the Phase 0 RomM work. RomM Swarm needs a
// specific set of things from a RomM server, and until a real server has been
// probed, the exact paths are an assumption. So the assumption is written down
// here, as data, with the Phase 0 deliverable each capability serves — rather
// than being scattered through client code as hard-coded URLs.
//
// When the probe runs against a real instance and a capability does not match,
// the fix is to add the real path to that capability's candidates. Nothing else
// changes, and the Phase 0 evidence records both what was expected and what was
// found.
//
// Candidate paths are matched against the server's own OpenAPI document with
// templated segments normalised to "*", so "/api/roms/{id}" and
// "/api/roms/{rom_id}" both match "/api/roms/*".

// Requirement is one capability RomM Swarm needs from a RomM server.
type Requirement struct {
	// ID is the stable capability name used in fixtures and reports.
	ID string

	// Summary says what the capability does.
	Summary string

	// Needs names the Scope of Work Phase 0 deliverable that depends on it.
	Needs string

	// Required marks a capability without which the MVP cannot proceed. A
	// missing required capability is a Phase 0 go/stop finding, not a warning.
	Required bool

	// Candidates are the operations that would satisfy the requirement. The
	// first match wins.
	Candidates []Candidate

	// WantsQueryParams are query parameters the matched operation is expected
	// to declare. Their absence is reported but does not fail the capability,
	// because a server may support them without documenting them.
	WantsQueryParams []string
}

// Candidate is one operation shape that would satisfy a requirement.
type Candidate struct {
	Method string
	// Path is a normalised path, where "*" matches one templated segment.
	Path string
}

// Requirements is the capability table.
func Requirements() []Requirement {
	return []Requirement{
		{
			ID:       "server.version",
			Summary:  "report the server's version and health",
			Needs:    "Confirm supported RomM versions and relevant API endpoints",
			Required: true,
			Candidates: []Candidate{
				{"GET", "/api/heartbeat"},
				{"GET", "/api/system/heartbeat"},
				{"GET", "/api/stats"},
				{"GET", "/heartbeat"},
			},
		},
		{
			ID:       "platforms.list",
			Summary:  "enumerate the platforms in the library",
			Needs:    "Confirm available hashes, metadata identifiers, file sizes, and platform identifiers",
			Required: true,
			Candidates: []Candidate{
				{"GET", "/api/platforms"},
			},
		},
		{
			ID:       "roms.list",
			Summary:  "enumerate ROMs with pagination",
			Needs:    "Complete inventory fetch with pagination; prototype a normalized local inventory manifest",
			Required: true,
			Candidates: []Candidate{
				{"GET", "/api/roms"},
			},
			WantsQueryParams: []string{"limit", "offset", "platform_id"},
		},
		{
			ID:       "roms.detail",
			Summary:  "read one ROM's metadata, hashes, and file size",
			Needs:    "Confirm available hashes, metadata identifiers, file sizes, and platform identifiers",
			Required: true,
			Candidates: []Candidate{
				{"GET", "/api/roms/*"},
			},
		},
		{
			ID:       "roms.download",
			Summary:  "download a ROM's content",
			Needs:    "Prove API-only mode: download, local staging and verification",
			Required: true,
			Candidates: []Candidate{
				{"GET", "/api/roms/*/content/*"},
				{"GET", "/api/roms/*/content"},
				{"GET", "/api/roms/*/download"},
			},
		},
		{
			ID:      "roms.upload",
			Summary: "start a chunked ROM upload session using a scoped standard-user token",
			Needs: "Verify standard-user Client API Token behavior for roms.write upload without administrator access; " +
				"prove API-only mode chunked upload to RomM",
			Required: true,
			Candidates: []Candidate{
				// Confirmed against a live RomM 5.0.0 instance: upload is a
				// chunked session, not a single-shot POST. The full sequence,
				// implemented in bridge/ingest.RommUploader:
				//   POST /api/roms/upload/start        four headers, returns upload_id
				//   PUT  /api/roms/upload/{upload_id}   one call per chunk, x-chunk-index header, raw body
				//   POST /api/roms/upload/{upload_id}/complete
				//   POST /api/roms/upload/{upload_id}/cancel   on failure, best effort
				// The three {upload_id} routes are not independently discoverable
				// without a live session, so this single entry point stands for
				// the whole subsystem.
				{"POST", "/api/roms/upload/start"},
				// Retained as fallback candidates for a RomM version that predates
				// the chunked upload session API. Unconfirmed against any real
				// server; kept only because they cost nothing to try.
				{"POST", "/api/roms"},
				{"PUT", "/api/roms"},
			},
		},
		{
			ID:       "identity.self",
			Summary:  "report the authenticated user and the scopes the token carries",
			Needs:    "Verify standard-user Client API Token behavior without administrator access",
			Required: true,
			Candidates: []Candidate{
				{"GET", "/api/users/me"},
				{"GET", "/api/me"},
				{"GET", "/api/users/current"},
			},
		},
		{
			ID:       "roms.scan",
			Summary:  "ask the server to rescan the library",
			Needs:    "Define the RomM ingestion state machine and timeout behavior",
			Required: false,
			Candidates: []Candidate{
				{"POST", "/api/scan"},
				{"POST", "/api/platforms/*/scan"},
				{"GET", "/api/scan"},
			},
		},
		{
			ID:       "roms.search",
			Summary:  "search the library, so ingestion can be confirmed without a full rescan",
			Needs:    "RomM ingestion and expected-identity reconciliation",
			Required: false,
			Candidates: []Candidate{
				{"GET", "/api/roms"},
			},
			WantsQueryParams: []string{"search_term"},
		},
	}
}

// CapabilityResult is the outcome of evaluating one requirement.
type CapabilityResult struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Needs    string `json:"needs"`
	Required bool   `json:"required"`

	// Found reports whether a candidate matched.
	Found bool `json:"found"`

	// Method and Path are the matched operation, as written in the server's
	// document.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`

	// OperationID is the server's own name for the operation, useful when
	// reporting a mismatch upstream.
	OperationID string `json:"operation_id,omitempty"`

	// MissingQueryParams lists expected query parameters the document does not
	// declare.
	MissingQueryParams []string `json:"missing_query_params,omitempty"`

	// Tried lists the candidate shapes that were looked for, so a report of a
	// missing capability says what was expected.
	Tried []string `json:"tried,omitempty"`
}

// EvaluateCapabilities matches the requirement table against a specification.
func EvaluateCapabilities(spec *Spec) []CapabilityResult {
	ops := spec.Operations()

	index := map[string]OperationRef{}
	for _, op := range ops {
		index[op.Method+" "+op.Normalized] = op
	}

	var out []CapabilityResult
	for _, req := range Requirements() {
		res := CapabilityResult{
			ID:       req.ID,
			Summary:  req.Summary,
			Needs:    req.Needs,
			Required: req.Required,
		}
		for _, c := range req.Candidates {
			res.Tried = append(res.Tried, c.Method+" "+c.Path)
		}

		for _, c := range req.Candidates {
			op, ok := index[strings.ToUpper(c.Method)+" "+c.Path]
			if !ok {
				continue
			}
			res.Found = true
			res.Method = op.Method
			res.Path = op.Path
			res.OperationID = op.Operation.OperationID
			for _, want := range req.WantsQueryParams {
				if !op.Operation.HasQueryParam(want) {
					res.MissingQueryParams = append(res.MissingQueryParams, want)
				}
			}
			break
		}
		out = append(out, res)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MissingRequired returns the required capabilities that were not found. A
// non-empty result is a Phase 0 go/stop finding.
func MissingRequired(results []CapabilityResult) []CapabilityResult {
	var out []CapabilityResult
	for _, r := range results {
		if r.Required && !r.Found {
			out = append(out, r)
		}
	}
	return out
}
