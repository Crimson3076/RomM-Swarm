package hostui

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func (s *Server) handleCreateSwarm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.dashboardError(w, r, "could not read the submitted form")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.dashboardError(w, r, "Swarm name is required")
		return
	}

	id, err := s.Directory.CreateSwarm(r.Context(), userFromContext(r.Context()), name)
	if err != nil {
		s.dashboardError(w, r, "could not create Swarm: "+err.Error())
		return
	}
	http.Redirect(w, r, "/swarms/"+string(id), http.StatusSeeOther)
}

// handleDeleteSwarm permanently deletes a Swarm — the confirmation is that
// the submitted confirm_name must match the Swarm's actual name exactly,
// so an owner can't fat-finger a delete the way a bare "are you sure?"
// button invites. See directory.DeleteSwarm's own doc comment for exactly
// what is and isn't removed.
func (s *Server) handleDeleteSwarm(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	account := userFromContext(r.Context())

	sw, err := s.Directory.GetSwarm(r.Context(), account, swarmID)
	if err != nil {
		s.swarmViewError(w, r, swarmID, "could not load Swarm: "+err.Error())
		return
	}

	if err := r.ParseForm(); err != nil {
		s.swarmViewError(w, r, swarmID, "could not read the submitted form")
		return
	}
	if r.FormValue("confirm_name") != sw.Name {
		s.swarmViewError(w, r, swarmID, "the typed name did not match \""+sw.Name+"\" — Swarm not deleted")
		return
	}

	if err := s.Directory.DeleteSwarm(r.Context(), account, swarmID); err != nil {
		s.swarmViewError(w, r, swarmID, "could not delete Swarm: "+err.Error())
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleDeleteInvitation permanently removes one invitation from the
// Swarm's list. See directory.DeleteInvitation's own doc comment.
func (s *Server) handleDeleteInvitation(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	invitationID := protocol.InvitationID(r.PathValue("invitationID"))

	if err := s.Directory.DeleteInvitation(r.Context(), swarmID, invitationID); err != nil {
		s.swarmViewError(w, r, swarmID, "could not delete invitation: "+err.Error())
		return
	}
	http.Redirect(w, r, "/swarms/"+string(swarmID), http.StatusSeeOther)
}

func (s *Server) dashboardError(w http.ResponseWriter, r *http.Request, msg string) {
	data := dashboardData{baseData: s.base(r, "Dashboard")}
	data.Error = msg
	if swarms, err := s.Directory.ListSwarms(r.Context(), userFromContext(r.Context())); err == nil {
		for _, sw := range swarms {
			data.Swarms = append(data.Swarms, swarmSummary{ID: string(sw.ID), Name: sw.Name})
		}
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	renderPage(w, "dashboard", data)
}

type swarmViewData struct {
	baseData
	Swarm       swarmSummary
	Invitations []invitationSummary
	Bridges     []bridgeSummary

	// Inventory* fields are Swarm-wide totals (ADR 0019). Zero across the
	// board is indistinguishable from "nothing published yet" — both are
	// legitimately zero, and the page says so rather than implying a
	// missing feature.
	InventoryDistinctFiles   int
	InventoryTotalReplicas   int
	InventoryDuplicatedFiles int

	// ReferenceCatalogues and UploadablePlatforms back the "Reference
	// catalogues" card (ADR 0023): what's currently loaded, and the full
	// supported-platform list for the upload form's selector, in that
	// fixed order rather than whatever ListReferenceCatalogues' SQL
	// ordering happens to be.
	ReferenceCatalogues []referenceCatalogueSummary
	UploadablePlatforms []string
}

type referenceCatalogueSummary struct {
	Platform   string
	Filename   string
	EntryCount int
	UploadedAt string
}

type invitationSummary struct {
	ID        string
	MaxUses   int
	UseCount  int
	ExpiresAt string
	Revoked   bool
	Status    string
}

type bridgeSummary struct {
	ID                string
	DisplayName       string
	JoinedAt          string
	State             string
	CredentialRevoked bool

	// ItemCount and LastPublishedAt reflect this Bridge's latest inventory
	// snapshot for this Swarm (ADR 0019). LastPublishedAt is empty when
	// this Bridge has never published.
	ItemCount       int
	LastPublishedAt string
}

func (s *Server) handleSwarmView(w http.ResponseWriter, r *http.Request) {
	data, ok := s.loadSwarmView(w, r)
	if !ok {
		return
	}
	renderPage(w, "swarm", data)
}

// loadSwarmView loads the swarm named by the request's {swarmID} path
// value, scoped to the requesting owner, plus its invitations and Bridges.
// On failure it writes the response itself (404 or 500) and returns
// ok=false — callers must stop immediately in that case.
func (s *Server) loadSwarmView(w http.ResponseWriter, r *http.Request) (swarmViewData, bool) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	sw, err := s.Directory.GetSwarm(r.Context(), userFromContext(r.Context()), swarmID)
	if errors.Is(err, directory.ErrSwarmNotFound) {
		http.NotFound(w, r)
		return swarmViewData{}, false
	}
	if err != nil {
		http.Error(w, "internal error loading Swarm", http.StatusInternalServerError)
		return swarmViewData{}, false
	}

	data := swarmViewData{
		baseData: s.base(r, sw.Name),
		Swarm:    swarmSummary{ID: string(sw.ID), Name: sw.Name},
	}

	invitations, err := s.Directory.ListInvitations(r.Context(), swarmID)
	if err != nil {
		data.Error = "listing invitations: " + err.Error()
	}
	for _, inv := range invitations {
		data.Invitations = append(data.Invitations, invitationSummary{
			ID:        string(inv.ID),
			MaxUses:   inv.MaxUses,
			UseCount:  inv.UseCount,
			ExpiresAt: inv.ExpiresAt.Format(time.RFC3339),
			Revoked:   !inv.RevokedAt.IsZero(),
			Status:    invitationStatus(inv, time.Now()),
		})
	}

	totals, snapshots, err := s.Directory.SwarmInventorySummary(r.Context(), swarmID)
	if err != nil {
		data.Error = "loading inventory: " + err.Error()
	}
	data.InventoryDistinctFiles = totals.DistinctFiles
	data.InventoryTotalReplicas = totals.TotalReplicas
	data.InventoryDuplicatedFiles = totals.DuplicatedFiles
	snapshotByBridge := make(map[protocol.BridgeID]hoststore.InventorySnapshot, len(snapshots))
	for _, snap := range snapshots {
		snapshotByBridge[snap.BridgeID] = snap
	}

	bridges, err := s.Directory.ListBridgesForSwarm(r.Context(), swarmID)
	if err != nil {
		data.Error = "listing Bridges: " + err.Error()
	}
	for _, b := range bridges {
		summary := bridgeSummary{
			ID:                string(b.BridgeID),
			DisplayName:       b.DisplayName,
			JoinedAt:          b.JoinedAt.Format(time.RFC3339),
			State:             b.State,
			CredentialRevoked: b.CredentialRevoked,
		}
		if snap, ok := snapshotByBridge[b.BridgeID]; ok {
			summary.ItemCount = snap.ItemCount
			summary.LastPublishedAt = snap.PublishedAt.Format(time.RFC3339)
		}
		data.Bridges = append(data.Bridges, summary)
	}

	catalogues, err := s.Directory.ListReferenceCatalogues(r.Context(), userFromContext(r.Context()), swarmID)
	if err != nil {
		data.Error = "listing reference catalogues: " + err.Error()
	}
	for _, c := range catalogues {
		data.ReferenceCatalogues = append(data.ReferenceCatalogues, referenceCatalogueSummary{
			Platform:   string(c.Platform),
			Filename:   c.Filename,
			EntryCount: c.EntryCount,
			UploadedAt: c.UploadedAt.Format(time.RFC3339),
		})
	}
	for _, p := range protocol.InitialPlatforms() {
		data.UploadablePlatforms = append(data.UploadablePlatforms, string(p))
	}

	return data, true
}

// maxReferenceCatalogueBytes bounds an uploaded DAT file — generous for a
// Logiqx catalogue (reference.ImportDAT's own doc comment: "a few
// megabytes at most"), while still refusing to buffer an unbounded upload
// into memory.
const maxReferenceCatalogueBytes = 32 << 20 // 32MiB

// handleUploadReferenceCatalogue stores or replaces swarm's catalogue for
// one platform (ADR 0023) — reference.ImportDAT validates the upload
// before anything is stored, so a bad file is rejected here with a clear
// error rather than silently failing on every Bridge's next fetch.
func (s *Server) handleUploadReferenceCatalogue(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	account := userFromContext(r.Context())

	platform := protocol.PlatformID(r.FormValue("platform"))

	file, header, err := r.FormFile("dat_file")
	if err != nil {
		s.swarmViewError(w, r, swarmID, "no file was uploaded: "+err.Error())
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxReferenceCatalogueBytes+1))
	if err != nil {
		s.swarmViewError(w, r, swarmID, "could not read the uploaded file: "+err.Error())
		return
	}
	if len(content) > maxReferenceCatalogueBytes {
		s.swarmViewError(w, r, swarmID, fmt.Sprintf("the uploaded file exceeds the %dMiB limit", maxReferenceCatalogueBytes>>20))
		return
	}

	if err := s.Directory.UploadReferenceCatalogue(r.Context(), account, swarmID, platform, header.Filename, content); err != nil {
		s.swarmViewError(w, r, swarmID, "could not upload the catalogue: "+err.Error())
		return
	}

	data, ok := s.loadSwarmView(w, r)
	if !ok {
		return
	}
	data.Notice = "Reference catalogue uploaded."
	renderPage(w, "swarm", data)
}

// handleDeleteReferenceCatalogue removes swarm's stored catalogue for one
// platform, if any.
func (s *Server) handleDeleteReferenceCatalogue(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	platform := protocol.PlatformID(r.PathValue("platform"))
	account := userFromContext(r.Context())

	if err := s.Directory.DeleteReferenceCatalogue(r.Context(), account, swarmID, platform); err != nil {
		s.swarmViewError(w, r, swarmID, "could not delete the catalogue: "+err.Error())
		return
	}
	http.Redirect(w, r, "/swarms/"+string(swarmID), http.StatusSeeOther)
}

func (s *Server) handleIssueInvitation(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	sw, err := s.Directory.GetSwarm(r.Context(), userFromContext(r.Context()), swarmID)
	if errors.Is(err, directory.ErrSwarmNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error loading Swarm", http.StatusInternalServerError)
		return
	}

	maxUses, days := 1, 7
	if raw := r.FormValue("max_uses"); raw != "" {
		maxUses, err = strconv.Atoi(raw)
		if err != nil || maxUses < 1 || maxUses > 100 {
			s.swarmViewError(w, r, swarmID, "Invitation uses must be between 1 and 100.")
			return
		}
	}
	if raw := r.FormValue("expires_days"); raw != "" {
		days, err = strconv.Atoi(raw)
		if err != nil || days < 1 || days > 30 {
			s.swarmViewError(w, r, swarmID, "Invitation expiry must be between 1 and 30 days.")
			return
		}
	}
	code, _, err := s.Directory.IssueInvitation(r.Context(), swarmID, userFromContext(r.Context()), maxUses, time.Duration(days)*24*time.Hour)
	if err != nil {
		s.swarmViewError(w, r, swarmID, "could not issue invitation: "+err.Error())
		return
	}

	renderPage(w, "secret_display", secretDisplayData{
		baseData:  s.base(r, "Invitation issued"),
		SwarmID:   string(sw.ID),
		SwarmName: sw.Name,
		Heading:   "Invitation issued",
		Secret:    string(code),
		Notice:    "This code will not be shown again. Give it to whoever is enrolling the new Bridge.",
	})
}

// swarmViewError re-renders the Swarm view page with an inline error,
// mirroring how bridge/adminui surfaces form failures — no separate flash
// mechanism across a redirect exists here.
func (s *Server) swarmViewError(w http.ResponseWriter, r *http.Request, swarmID protocol.SwarmID, msg string) {
	data, ok := s.loadSwarmView(w, r)
	if !ok {
		return
	}
	data.Error = msg
	w.WriteHeader(http.StatusUnprocessableEntity)
	renderPage(w, "swarm", data)
}

// secretDisplayData renders a one-time secret — an invitation code or a
// re-enrollment refresh token — exactly once, never held server-side
// beyond this single response. Shared by handleIssueInvitation and
// handleReenrollBridge.
type secretDisplayData struct {
	baseData
	SwarmID   string
	SwarmName string
	Heading   string
	Secret    string
	Notice    string
}

func invitationStatus(inv directory.Invitation, now time.Time) string {
	switch {
	case !inv.RevokedAt.IsZero():
		return "revoked"
	case !inv.ExpiresAt.After(now):
		return "expired"
	case inv.UseCount >= inv.MaxUses:
		return "exhausted"
	default:
		return "active"
	}
}
