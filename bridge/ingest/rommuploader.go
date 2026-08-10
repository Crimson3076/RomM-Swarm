package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
)

// RommUploader implements Uploader against RomM's real chunked upload API.
//
// The whole protocol below was confirmed against a live RomM 5.0.0 instance —
// not inferred from its OpenAPI schema alone, which left two things the schema
// could not answer: the field name carrying the upload session id in start's
// response, and whether a chunk is sent as a raw byte stream or wrapped in
// something else. Both were resolved by performing one real upload and reading
// the server's own logs. See docs/adr/0003-supported-romm-versions.md and
// docs/phase0/multi-file-archive-and-ingestion-behavior.md.
//
// The confirmed sequence:
//
//	POST /api/roms/upload/start
//	  headers: x-upload-platform (RomM's numeric platform id), x-upload-filename,
//	           x-upload-total-size, x-upload-total-chunks
//	  response: {"upload_id": "<uuid>", ...}
//
//	PUT /api/roms/upload/{upload_id}      (once per chunk)
//	  header: x-chunk-index (0-based)
//	  body:   raw chunk bytes, Content-Type: application/octet-stream
//	  response: {"received": N, "total": N}
//
//	POST /api/roms/upload/{upload_id}/complete
//	  response: 2xx, body may be empty. Assembly happens here; RomM's own
//	  filesystem watcher then discovers the file and schedules a scan — on the
//	  confirmed instance, a change-triggered rescan is debounced five minutes.
//	  This is why source activation waits on Reconciler rather than trusting
//	  a successful complete call by itself.
//
//	POST /api/roms/upload/{upload_id}/cancel   (best effort, on failure)
type RommUploader struct {
	Client    *romm.Client
	Report    *romm.Report
	Platforms *romm.Platforms

	// ChunkSize bounds how much of the file is held in memory at once and how
	// large a single PUT body is. Zero means DefaultChunkSize.
	ChunkSize int64
}

// DefaultChunkSize is a conservative default. RomM's confirmed behaviour did
// not reveal any documented maximum, so this stays well under typical proxy
// and load-balancer body-size limits (Cloudflare's plan-dependent request-body
// caps included) rather than assuming an initial platform's largest file (a
// Nintendo DS cartridge, up to 512 MiB) can go through in one chunk.
const DefaultChunkSize = 8 << 20

func (u *RommUploader) chunkSize() int64 {
	if u.ChunkSize > 0 {
		return u.ChunkSize
	}
	return DefaultChunkSize
}

// startResponse is the subset of start's response this Bridge reads. RomM's
// own OpenAPI document declares the rest of the object as untyped
// (additionalProperties: true), so only the confirmed field is decoded.
type startResponse struct {
	UploadID string `json:"upload_id"`
}

// Upload implements Uploader.
func (u *RommUploader) Upload(ctx context.Context, stagedPath, filename string, expected Expected) error {
	if u.Client == nil || u.Report == nil || u.Platforms == nil {
		return errors.New("ingest: RommUploader requires a Client, a Report, and Platforms")
	}

	platform, err := u.Platforms.Lookup(string(expected.Platform))
	if err != nil {
		return err
	}

	f, err := os.Open(stagedPath)
	if err != nil {
		return fmt.Errorf("ingest: opening the staged payload: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("ingest: reading the staged payload's size: %w", err)
	}
	size := info.Size()
	if size <= 0 {
		return fmt.Errorf("ingest: staged payload is empty")
	}

	totalChunks := (size + u.chunkSize() - 1) / u.chunkSize()

	uploadID, err := u.start(ctx, platform.ID, filename, size, totalChunks)
	if err != nil {
		return fmt.Errorf("ingest: starting the upload session: %w", err)
	}

	if err := u.sendChunks(ctx, uploadID, f, size); err != nil {
		u.cancel(uploadID)
		return fmt.Errorf("ingest: uploading chunks: %w", err)
	}

	if err := u.complete(ctx, uploadID); err != nil {
		u.cancel(uploadID)
		return fmt.Errorf("ingest: completing the upload: %w", err)
	}
	return nil
}

func (u *RommUploader) start(ctx context.Context, platformID int, filename string, size, totalChunks int64) (string, error) {
	cap, err := u.Report.Capability("roms.upload")
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.Client.BaseURL+cap.Path, nil)
	if err != nil {
		return "", fmt.Errorf("building the start request: %w", err)
	}
	req.Header.Set("x-upload-platform", strconv.Itoa(platformID))
	req.Header.Set("x-upload-filename", filename)
	req.Header.Set("x-upload-total-size", strconv.FormatInt(size, 10))
	req.Header.Set("x-upload-total-chunks", strconv.FormatInt(totalChunks, 10))
	if u.Client.Token != "" && u.Client.Scheme.Apply != nil {
		u.Client.Scheme.Apply(req, u.Client.Token)
	}

	resp, err := u.Client.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s", u.Client.Redact(err.Error()))
	}
	defer resp.Body.Close()

	body, err := readCapped(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", apiError(resp.StatusCode, body)
	}

	var parsed startResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("could not parse the start response: %w", err)
	}
	if parsed.UploadID == "" {
		return "", fmt.Errorf("the start response carried no upload_id: %s", truncate(body, 200))
	}
	return parsed.UploadID, nil
}

func (u *RommUploader) sendChunks(ctx context.Context, uploadID string, f *os.File, size int64) error {
	cap, err := u.Report.Capability("roms.upload")
	if err != nil {
		return err
	}
	// The chunk route is templated on the upload_id start returned, not
	// independently discoverable — built directly from the confirmed shape
	// rather than through the single-placeholder substitution bridge/scan uses
	// for roms.download, which assumes a fixed template this session doesn't
	// have.
	basePath := uploadPathBase(cap.Path)

	index := int64(0)
	for offset := int64(0); offset < size; offset += u.chunkSize() {
		n := u.chunkSize()
		if remaining := size - offset; n > remaining {
			n = remaining
		}

		if err := u.putChunk(ctx, basePath+"/"+uploadID, index, io.NewSectionReader(f, offset, n), n); err != nil {
			return fmt.Errorf("chunk %d: %w", index, err)
		}
		index++

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return nil
}

func (u *RommUploader) putChunk(ctx context.Context, path string, index int64, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.Client.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("building the chunk request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("x-chunk-index", strconv.FormatInt(index, 10))
	req.Header.Set("Content-Type", "application/octet-stream")
	if u.Client.Token != "" && u.Client.Scheme.Apply != nil {
		u.Client.Scheme.Apply(req, u.Client.Token)
	}

	resp, err := u.Client.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s", u.Client.Redact(err.Error()))
	}
	defer resp.Body.Close()

	respBody, err := readCapped(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return apiError(resp.StatusCode, respBody)
	}
	return nil
}

func (u *RommUploader) complete(ctx context.Context, uploadID string) error {
	cap, err := u.Report.Capability("roms.upload")
	if err != nil {
		return err
	}
	path := uploadPathBase(cap.Path) + "/" + uploadID + "/complete"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.Client.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("building the complete request: %w", err)
	}
	if u.Client.Token != "" && u.Client.Scheme.Apply != nil {
		u.Client.Scheme.Apply(req, u.Client.Token)
	}

	resp, err := u.Client.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s", u.Client.Redact(err.Error()))
	}
	defer resp.Body.Close()

	body, err := readCapped(resp.Body)
	if err != nil {
		return err
	}
	// A successful complete call was confirmed to return an empty body, so an
	// empty body is not itself an error — only a non-2xx status is. See the
	// upload_id-scoped comment above for how this was established.
	if resp.StatusCode/100 != 2 {
		return apiError(resp.StatusCode, body)
	}
	return nil
}

// cancel is best effort: a failed cancel must never mask the original error
// that caused it to be called, and is not itself returned.
func (u *RommUploader) cancel(uploadID string) {
	cap, err := u.Report.Capability("roms.upload")
	if err != nil {
		return
	}
	path := uploadPathBase(cap.Path) + "/" + uploadID + "/cancel"

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u.Client.BaseURL+path, nil)
	if err != nil {
		return
	}
	if u.Client.Token != "" && u.Client.Scheme.Apply != nil {
		u.Client.Scheme.Apply(req, u.Client.Token)
	}
	resp, err := u.Client.HTTP.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// uploadPathBase strips the confirmed "/start" suffix from the roms.upload
// capability path, leaving the base every {upload_id}-scoped route is built
// from ("/api/roms/upload").
func uploadPathBase(startPath string) string {
	const suffix = "/start"
	if len(startPath) > len(suffix) && startPath[len(startPath)-len(suffix):] == suffix {
		return startPath[:len(startPath)-len(suffix)]
	}
	return startPath
}

// readCapped reads a response body with a small bound. Every response this
// uploader reads is either a short status object or empty; a large body here
// would itself be a sign something unexpected is happening.
func readCapped(r io.Reader) ([]byte, error) {
	const max = 64 << 10
	b, err := io.ReadAll(io.LimitReader(r, max))
	if err != nil {
		return nil, fmt.Errorf("reading the response: %w", err)
	}
	return b, nil
}

// apiError renders a non-2xx response, extracting FastAPI's conventional
// {"detail": "..."} shape when present — confirmed as the real error shape
// RomM returns (observed for both an auth rejection and an expired upload
// session) — and falling back to the raw body otherwise.
func apiError(status int, body []byte) error {
	var withDetail struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &withDetail) == nil && len(withDetail.Detail) > 0 {
		return fmt.Errorf("romm returned status %d: %s", status, withDetail.Detail)
	}
	return fmt.Errorf("romm returned status %d: %s", status, truncate(body, 200))
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
