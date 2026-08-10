package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
)

// RommSource lists and downloads a RomM server's inventory using paths a
// capability probe has already resolved.
//
// It never guesses a path on its own. Every request is built from
// romm.CapabilityResult.Path — the exact operation the target server's own
// OpenAPI document named. If a probe could not resolve roms.list or
// roms.download on the server it ran against, RommSource refuses to run rather
// than falling back to a hard-coded guess: a second, independent guess about
// RomM's URL shape is exactly what the capability table exists to make
// unnecessary. See docs/adr/0003-supported-romm-versions.md.
type RommSource struct {
	Client *romm.Client
	Report *romm.Report

	// MaxDownloadBytes bounds how large a single downloaded item may be. Zero
	// means DefaultMaxDownloadBytes.
	MaxDownloadBytes int64
}

// DefaultMaxDownloadBytes is generous enough for every initial platform —
// the largest is a Nintendo DS cartridge at 512 MiB — with headroom, while
// still refusing to stream an unbounded response from a misbehaving or
// compromised server.
const DefaultMaxDownloadBytes = 1 << 30

func (s *RommSource) maxDownload() int64 {
	if s.MaxDownloadBytes > 0 {
		return s.MaxDownloadBytes
	}
	return DefaultMaxDownloadBytes
}

// ListROMs implements Source.
func (s *RommSource) ListROMs(ctx context.Context, page Page) ([]ROMRecord, bool, error) {
	cap, err := s.Report.Capability("roms.list")
	if err != nil {
		return nil, false, err
	}
	if strings.Contains(cap.Path, "{") {
		return nil, false, fmt.Errorf("scan: roms.list resolved to %q, which expects a path parameter; a listing endpoint should not", cap.Path)
	}

	q := url.Values{}
	if page.Limit > 0 {
		q.Set("limit", strconv.Itoa(page.Limit))
	}
	q.Set("offset", strconv.Itoa(page.Offset))

	status, body, err := s.Client.Get(ctx, cap.Path, q)
	if err != nil {
		return nil, false, err
	}
	if status != http.StatusOK {
		return nil, false, fmt.Errorf("scan: %s returned status %d", cap.Path, status)
	}

	objects, total, err := decodeListing(body)
	if err != nil {
		return nil, false, fmt.Errorf("scan: decoding %s: %w", cap.Path, err)
	}

	records := make([]ROMRecord, 0, len(objects))
	for _, obj := range objects {
		records = append(records, recordFromObject(obj))
	}

	hasMore := page.Limit > 0 && len(objects) >= page.Limit
	if total >= 0 {
		hasMore = page.Offset+len(objects) < total
	}
	return records, hasMore, nil
}

// Download implements Source.
func (s *RommSource) Download(ctx context.Context, rec ROMRecord) (io.ReadCloser, int64, error) {
	cap, err := s.Report.Capability("roms.download")
	if err != nil {
		return nil, 0, err
	}

	path, err := substitutePath(cap.Path, rec.ID, rec.FSName)
	if err != nil {
		return nil, 0, fmt.Errorf("scan: building a download path from %q: %w", cap.Path, err)
	}

	status, body, contentLength, err := s.Client.Stream(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		body.Close()
		return nil, 0, fmt.Errorf("scan: %s returned status %d", path, status)
	}
	if contentLength > s.maxDownload() {
		body.Close()
		return nil, 0, fmt.Errorf("scan: %s declares %d bytes, beyond the %d-byte limit", path, contentLength, s.maxDownload())
	}
	return body, contentLength, nil
}

// substitutePath fills a template's {param} placeholders with values in order.
//
// RomM's download path carries either one placeholder (an id) or two (an id
// and a filename) across the candidate shapes in capability.go. Rather than
// hard-code which name means what — a third guess layered on the two the
// capability table already makes — every placeholder is simply filled
// positionally, which works for both shapes without knowing their names.
func substitutePath(template string, values ...string) (string, error) {
	var b strings.Builder
	vi := 0
	for i := 0; i < len(template); i++ {
		if template[i] != '{' {
			b.WriteByte(template[i])
			continue
		}
		end := strings.IndexByte(template[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated path parameter in %q", template)
		}
		if vi >= len(values) {
			return "", fmt.Errorf("%q has more path parameters than values were provided", template)
		}
		b.WriteString(url.PathEscape(values[vi]))
		vi++
		i += end
	}
	if vi == 0 {
		return "", fmt.Errorf("%q has no path parameters, but at least one value was required", template)
	}
	return b.String(), nil
}

// decodeListing parses a RomM listing response, which may be a bare array or
// an envelope carrying the array under one of several conventional keys.
// total is -1 when no total could be determined.
func decodeListing(body []byte) (objects []map[string]any, total int, err error) {
	total = -1

	var asArray []map[string]any
	if json.Unmarshal(body, &asArray) == nil {
		return asArray, total, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, total, err
	}
	for _, key := range []string{"items", "results", "data", "roms"} {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, v := range list {
			if obj, ok := v.(map[string]any); ok {
				objects = append(objects, obj)
			}
		}
		break
	}
	for _, key := range []string{"total", "count", "total_count"} {
		if v, ok := envelope[key].(float64); ok {
			total = int(v)
			break
		}
	}
	return objects, total, nil
}

// recordFromObject maps a listing entry to a ROMRecord, tolerant of the field
// name variation RomM versions are known to have. See
// docs/phase0/multi-file-archive-and-ingestion-behavior.md.
func recordFromObject(obj map[string]any) ROMRecord {
	rec := ROMRecord{
		ID:           fieldString(obj, "id", "rom_id"),
		PlatformSlug: fieldString(obj, "platform_slug", "platform_fs_slug"),
		Name:         fieldString(obj, "name"),
		FSName:       fieldString(obj, "fs_name", "file_name", "filename"),
		FSSizeBytes:  fieldInt64(obj, "fs_size_bytes", "file_size_bytes", "size"),
		Hashes:       map[string]string{},
	}
	for k, v := range obj {
		if str, ok := v.(string); ok && str != "" && romm.IsHashField(k) {
			rec.Hashes[k] = str
		}
	}
	return rec
}

func fieldString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		}
	}
	return ""
}

func fieldInt64(obj map[string]any, keys ...string) int64 {
	for _, k := range keys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int64(t)
		case string:
			if n, err := strconv.ParseInt(t, 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}
