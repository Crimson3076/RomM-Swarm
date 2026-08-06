package romm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// The capability probe.
//
// Scope of Work §13.3: "Build the RomM API capability probe and record fixtures
// from the selected supported versions." The output is a fixture bundle that
// can be committed and replayed, so CI proves the Bridge's expectations against
// a real server's shape without needing that server.
//
// What the bundle deliberately does not contain:
//
//   - The credential. Redacted at the client boundary.
//   - The server's hostname, unless the operator opts in. A fixture is likely to
//     be committed, and a private server's address is exactly the kind of detail
//     Scope of Work §7 asks to keep out of permanent records.
//   - Library contents. Field names and JSON types are recorded; values are not,
//     unless the operator opts in. Knowing that a ROM object has a "sha1_hash"
//     string is what the Bridge needs; knowing which games someone owns is not.

// SpecPaths are the locations a RomM server may publish its OpenAPI document
// at, tried in order.
var SpecPaths = []string{"/openapi.json", "/api/openapi.json", "/docs/openapi.json"}

// ProbeOptions configure a probe run.
type ProbeOptions struct {
	BaseURL string
	Token   string

	// IncludeHost records the server's address in the bundle.
	IncludeHost bool

	// IncludeSamples records one sample object's values, not just its shape.
	// Off by default: the sample is a real entry from a real library.
	IncludeSamples bool

	// HTTP overrides the default client, for tests.
	HTTP *http.Client
}

// FieldShape describes one field observed on a server object.
type FieldShape struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Report is the probe's findings.
type Report struct {
	SchemaVersion   int       `json:"schema_version"`
	ProtocolVersion string    `json:"protocol_version"`
	ProbedAt        time.Time `json:"probed_at"`

	// Host is the server address, or "[redacted]".
	Host string `json:"host"`

	SpecPath    string `json:"spec_path,omitempty"`
	SpecTitle   string `json:"spec_title,omitempty"`
	SpecVersion string `json:"spec_version,omitempty"`
	OpenAPI     string `json:"openapi,omitempty"`

	// ServerVersion is whatever version string the health endpoint reported.
	ServerVersion string `json:"server_version,omitempty"`

	// AuthScheme is the credential presentation the server accepted.
	AuthScheme string `json:"auth_scheme,omitempty"`

	Capabilities []CapabilityResult `json:"capabilities"`

	// PathInventory is every operation the server documents, normalised. This is
	// what makes a failed capability match fixable: the real path is right here.
	PathInventory []string `json:"path_inventory,omitempty"`

	// ROMFields and PlatformFields are the observed object shapes.
	ROMFields      []FieldShape `json:"rom_fields,omitempty"`
	PlatformFields []FieldShape `json:"platform_fields,omitempty"`

	// HashFields are the hash-bearing fields found on a ROM object. Phase 0
	// deliverable: "Confirm available hashes, metadata identifiers, file sizes,
	// and platform identifiers."
	HashFields []string `json:"hash_fields,omitempty"`

	// Sample is one ROM object, present only when explicitly requested.
	Sample map[string]any `json:"sample,omitempty"`

	// Findings are operator-facing observations, including every failure. A
	// probe that could not complete still produces a report; an empty report
	// would be indistinguishable from a probe that was never run.
	Findings []string `json:"findings"`

	// Blockers are the findings that stop Phase 1 under the go/stop rule.
	Blockers []string `json:"blockers,omitempty"`
}

// hashFieldCandidates are the field names that carry content hashes across RomM
// versions. Matching by suffix as well as by exact name, because the prefix
// convention has changed between releases.
var hashFieldCandidates = []string{
	"crc_hash", "md5_hash", "sha1_hash", "sha256_hash",
	"crc", "md5", "sha1", "sha256", "crc32",
}

// Probe runs the capability probe against a live server.
func Probe(ctx context.Context, opts ProbeOptions) (*Report, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, fmt.Errorf("romm: no server URL was given")
	}

	rep := &Report{
		SchemaVersion:   protocol.SchemaVersion,
		ProtocolVersion: protocol.Version(),
		ProbedAt:        time.Now().UTC(),
		Host:            "[redacted]",
	}
	if opts.IncludeHost {
		rep.Host = opts.BaseURL
	}

	client := NewClient(opts.BaseURL, opts.Token, AuthScheme{})
	if opts.HTTP != nil {
		client.HTTP = opts.HTTP
	}

	// The specification. Everything else depends on it.
	spec, specPath, err := fetchSpec(ctx, client)
	if err != nil {
		rep.Findings = append(rep.Findings, err.Error())
		rep.Blockers = append(rep.Blockers,
			"the server did not publish a readable OpenAPI document, so its capabilities cannot be confirmed")
		return rep, nil
	}
	rep.SpecPath = specPath
	rep.SpecTitle = spec.Info.Title
	rep.SpecVersion = spec.Info.Version
	rep.OpenAPI = spec.OpenAPI

	for _, op := range spec.Operations() {
		rep.PathInventory = append(rep.PathInventory, op.Method+" "+op.Normalized)
	}
	rep.PathInventory = dedupe(rep.PathInventory)

	rep.Capabilities = EvaluateCapabilities(spec)
	for _, missing := range MissingRequired(rep.Capabilities) {
		rep.Blockers = append(rep.Blockers, fmt.Sprintf(
			"required capability %q was not found (needed to %s). Looked for: %s. The server's documented paths are in path_inventory",
			missing.ID, missing.Summary, strings.Join(missing.Tried, ", ")))
	}
	for _, c := range rep.Capabilities {
		if c.Found && len(c.MissingQueryParams) > 0 {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"%s matched %s %s but does not document the query parameters %s; pagination or filtering may need to be confirmed by hand",
				c.ID, c.Method, c.Path, strings.Join(c.MissingQueryParams, ", ")))
		}
	}

	// The credential. Probed against a capability that requires one.
	authProbePath := capabilityPath(rep.Capabilities, "identity.self")
	if authProbePath == "" {
		authProbePath = capabilityPath(rep.Capabilities, "platforms.list")
	}
	if authProbePath == "" {
		rep.Findings = append(rep.Findings,
			"no authenticated endpoint was identified, so the credential presentation could not be confirmed")
	} else if opts.Token == "" {
		rep.Findings = append(rep.Findings,
			"no credential was supplied, so only the published specification was examined")
	} else {
		scheme, status, err := DetectAuthScheme(ctx, opts.BaseURL, opts.Token, authProbePath, client.HTTP)
		if err != nil {
			rep.Findings = append(rep.Findings, client.Redact(err.Error()))
			rep.Blockers = append(rep.Blockers,
				"the supplied credential was not accepted, so standard-user token behaviour could not be verified")
		} else {
			rep.AuthScheme = scheme.ID
			client.Scheme = scheme
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"the server accepted the credential presented as %s (status %d)", scheme.ID, status))
		}
	}

	// Live shape probes. Each failure is a finding, never a fatal error: a
	// partial report is more useful than none.
	if v := probeServerVersion(ctx, client, rep.Capabilities); v != "" {
		rep.ServerVersion = v
	} else {
		rep.Findings = append(rep.Findings, "the server did not report a version string")
	}

	if client.Scheme.Apply != nil {
		rep.PlatformFields = probeFields(ctx, client, rep, "platforms.list", nil)

		q := url.Values{}
		q.Set("limit", "1")
		rep.ROMFields = probeFields(ctx, client, rep, "roms.list", q)

		for _, f := range rep.ROMFields {
			if isHashField(f.Name) {
				rep.HashFields = append(rep.HashFields, f.Name)
			}
		}
		sort.Strings(rep.HashFields)

		if len(rep.ROMFields) > 0 && len(rep.HashFields) == 0 {
			rep.Blockers = append(rep.Blockers,
				"no hash field was found on a ROM object, so held content cannot be matched against a reference catalogue")
		}
		if opts.IncludeSamples {
			rep.Sample = probeSample(ctx, client, rep)
		}
	}

	return rep, nil
}

// fetchSpec tries each known specification location.
func fetchSpec(ctx context.Context, c *Client) (*Spec, string, error) {
	var attempts []string
	for _, p := range SpecPaths {
		status, body, err := c.GetUnauthenticated(ctx, p)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("%s: %s", p, c.Redact(err.Error())))
			continue
		}
		if status != http.StatusOK {
			attempts = append(attempts, fmt.Sprintf("%s: status %d", p, status))
			continue
		}
		spec, err := ParseSpec(body)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("%s: %s", p, err.Error()))
			continue
		}
		return spec, p, nil
	}
	return nil, "", fmt.Errorf("no OpenAPI document could be read (%s)", strings.Join(attempts, "; "))
}

// capabilityPath returns the matched path for a capability id.
func capabilityPath(results []CapabilityResult, id string) string {
	for _, r := range results {
		if r.ID == id && r.Found {
			return r.Path
		}
	}
	return ""
}

// probeServerVersion reads a version string from the health endpoint.
func probeServerVersion(ctx context.Context, c *Client, caps []CapabilityResult) string {
	path := capabilityPath(caps, "server.version")
	if path == "" {
		return ""
	}
	status, body, err := c.Get(ctx, path, nil)
	if err != nil || status != http.StatusOK {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	// The field name has moved between releases, so several are accepted.
	for _, key := range []string{"VERSION", "version", "romm_version", "app_version"} {
		if v, ok := payload[key].(string); ok && v != "" {
			return v
		}
	}
	// Some versions nest it one level down.
	for _, v := range payload {
		if nested, ok := v.(map[string]any); ok {
			for _, key := range []string{"VERSION", "version"} {
				if s, ok := nested[key].(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// probeFields calls a listing endpoint and records the shape of its objects.
func probeFields(ctx context.Context, c *Client, rep *Report, capID string, q url.Values) []FieldShape {
	path := capabilityPath(rep.Capabilities, capID)
	if path == "" {
		return nil
	}
	status, body, err := c.Get(ctx, path, q)
	if err != nil {
		rep.Findings = append(rep.Findings, fmt.Sprintf("%s: %s", capID, c.Redact(err.Error())))
		return nil
	}
	if status != http.StatusOK {
		rep.Findings = append(rep.Findings, fmt.Sprintf("%s: %s returned status %d", capID, path, status))
		return nil
	}
	obj := firstObject(body)
	if obj == nil {
		rep.Findings = append(rep.Findings, fmt.Sprintf("%s: %s returned no objects to inspect", capID, path))
		return nil
	}
	return shapeOf(obj)
}

// probeSample returns one whole ROM object.
func probeSample(ctx context.Context, c *Client, rep *Report) map[string]any {
	path := capabilityPath(rep.Capabilities, "roms.list")
	if path == "" {
		return nil
	}
	q := url.Values{}
	q.Set("limit", "1")
	status, body, err := c.Get(ctx, path, q)
	if err != nil || status != http.StatusOK {
		return nil
	}
	return firstObject(body)
}

// firstObject extracts the first object from a response that may be a bare
// array or an envelope with an items field.
func firstObject(body []byte) map[string]any {
	var asArray []map[string]any
	if json.Unmarshal(body, &asArray) == nil && len(asArray) > 0 {
		return asArray[0]
	}

	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	for _, key := range []string{"items", "results", "data", "roms", "platforms"} {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok || len(list) == 0 {
			continue
		}
		if obj, ok := list[0].(map[string]any); ok {
			return obj
		}
	}
	// A single object response.
	if len(envelope) > 0 {
		return envelope
	}
	return nil
}

// shapeOf records field names and JSON types, never values.
func shapeOf(obj map[string]any) []FieldShape {
	out := make([]FieldShape, 0, len(obj))
	for k, v := range obj {
		out = append(out, FieldShape{Name: k, Type: jsonType(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

// isHashField reports whether a field name looks like a content hash.
func isHashField(name string) bool {
	lower := strings.ToLower(name)
	for _, c := range hashFieldCandidates {
		if lower == c || strings.HasSuffix(lower, "_"+c) {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
