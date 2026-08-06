// Package romm is the Bridge's adapter around one RomM server.
//
// Scope of Work §7 (Compatibility) requires the Bridge to "Detect RomM API
// version and capabilities", to "Use RomM OpenAPI specifications where
// practical", and to "Maintain adapter boundaries around RomM-specific
// endpoints". This package is that boundary: nothing outside it knows what
// RomM's URLs look like.
//
// The capability probe is built around the specification the server publishes
// rather than around a hard-coded list of endpoints. That choice is what makes
// the Phase 0 deliverable "Confirm supported RomM versions and relevant API
// endpoints" answerable as evidence rather than as an assertion — the probe
// reports what a specific server actually offers, and the requirement table
// below records what RomM Swarm needs and why.
package romm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Spec is the subset of an OpenAPI document the probe reads.
type Spec struct {
	OpenAPI string              `json:"openapi"`
	Info    SpecInfo            `json:"info"`
	Paths   map[string]PathItem `json:"paths"`
}

// SpecInfo is the document's info block.
type SpecInfo struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

// PathItem holds the operations defined for one path.
type PathItem map[string]Operation

// Operation is one method on one path.
type Operation struct {
	OperationID string                `json:"operationId"`
	Summary     string                `json:"summary"`
	Tags        []string              `json:"tags"`
	Parameters  []Parameter           `json:"parameters"`
	Security    []map[string][]string `json:"security"`
}

// Parameter is one operation parameter.
type Parameter struct {
	Name string `json:"name"`
	In   string `json:"in"`
}

// ParseSpec decodes an OpenAPI document.
func ParseSpec(raw []byte) (*Spec, error) {
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("romm: parsing the OpenAPI document: %w", err)
	}
	if len(s.Paths) == 0 {
		return nil, fmt.Errorf("romm: the OpenAPI document declares no paths")
	}
	return &s, nil
}

// NormalizePath replaces templated segments with a wildcard, so that
// "/api/roms/{id}" and "/api/roms/{rom_id}" compare equal.
func NormalizePath(p string) string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, "*")
			continue
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

// OperationRef is one operation found in a specification.
type OperationRef struct {
	Method string
	// Path is the path as written in the document.
	Path string
	// Normalized is the path with templated segments replaced by wildcards.
	Normalized string
	Operation  Operation
}

// Operations returns every operation in the document, in a stable order.
func (s *Spec) Operations() []OperationRef {
	var out []OperationRef
	for path, item := range s.Paths {
		for method, op := range item {
			m := strings.ToUpper(method)
			switch m {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
			default:
				// "parameters" and vendor extensions live alongside operations.
				continue
			}
			out = append(out, OperationRef{
				Method:     m,
				Path:       path,
				Normalized: NormalizePath(path),
				Operation:  op,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// HasQueryParam reports whether an operation declares a query parameter.
func (o Operation) HasQueryParam(name string) bool {
	for _, p := range o.Parameters {
		if p.In == "query" && strings.EqualFold(p.Name, name) {
			return true
		}
	}
	return false
}
