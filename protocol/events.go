package protocol

import (
	"fmt"
	"sort"
	"time"
)

// The minimum event vocabulary (Scope of Work, Phase 0: "Define the minimum
// event vocabulary").
//
// Every event carries a retention class. This is not documentation: retention
// is a property of the event kind, declared at the point the kind is defined,
// so that adding a new event forces an explicit answer to "how long may the
// Host keep this, and does it name a title?".
//
// Scope of Work §7 requires that exact request and transfer titles use short
// documented retention, that long-term metrics are aggregate and title-free,
// and that raw IP addresses are not permanent history. Encoding retention in
// the vocabulary is what makes those rules enforceable by a sweeper that walks
// the event log, instead of a promise nobody can audit.

// Retention classifies how long an event may be kept in identifiable form.
type Retention string

const (
	// RetentionOperational is short-lived operational detail that may name an
	// exact title, a peer, or an address. Deleted or aggregated when the
	// operational window expires. See docs/phase0/retention-schedule.md.
	RetentionOperational Retention = "operational"

	// RetentionSecurity is security-relevant detail with a short, separately
	// documented window: authentication failures, token reuse, grant replay.
	// May contain an IP address; never becomes permanent history.
	RetentionSecurity Retention = "security"

	// RetentionAggregate survives long term because it carries no exact title
	// and no personal detail: counts, byte totals, coverage snapshots.
	RetentionAggregate Retention = "aggregate"

	// RetentionAudit is immutable administrative and moderation history, kept
	// for accountability. Names actors and actions, never search terms or the
	// contents of a member's library.
	RetentionAudit Retention = "audit"
)

// EventKind is a member of the closed event vocabulary.
type EventKind string

// Bridge lifecycle.
const (
	EventBridgeEnrolled EventKind = "bridge.enrolled"
	EventBridgeApproved EventKind = "bridge.approved"
	EventBridgeOnline   EventKind = "bridge.online"
	EventBridgeOffline  EventKind = "bridge.offline"
	EventBridgeStale    EventKind = "bridge.stale"
	EventBridgeDisabled EventKind = "bridge.disabled"
	EventBridgeRevoked  EventKind = "bridge.revoked"
)

// Credential lifecycle.
const (
	EventTokenIssued      EventKind = "token.issued"
	EventTokenRotated     EventKind = "token.rotated"
	EventTokenGraceUsed   EventKind = "token.grace_used"
	EventTokenReuseDetect EventKind = "token.reuse_detected"
	EventTokenFamilyRevkd EventKind = "token.family_revoked"
)

// Inventory publication.
const (
	EventInventorySnapshot  EventKind = "inventory.snapshot"
	EventInventoryDelta     EventKind = "inventory.delta"
	EventInventoryTombstone EventKind = "inventory.tombstone"
	EventInventoryReconcile EventKind = "inventory.reconcile"
	EventInventoryDrift     EventKind = "inventory.drift_detected"
)

// Access and transfer.
const (
	EventAccessRequested EventKind = "access.requested"
	EventAccessApproved  EventKind = "access.approved"
	EventAccessDenied    EventKind = "access.denied"
	EventGrantIssued     EventKind = "grant.issued"
	EventGrantExpired    EventKind = "grant.expired"
	EventGrantRevoked    EventKind = "grant.revoked"
	EventGrantReplay     EventKind = "grant.replay_detected"

	EventTransferStarted   EventKind = "transfer.started"
	EventTransferResumed   EventKind = "transfer.resumed"
	EventTransferRouted    EventKind = "transfer.routed"
	EventTransferCompleted EventKind = "transfer.completed"
	EventTransferFailed    EventKind = "transfer.failed"
	EventTransferCancelled EventKind = "transfer.cancelled"

	// EventDestinationState reports one move of the destination state machine.
	EventDestinationState EventKind = "destination.state_changed"
)

// Verification and preservation.
const (
	EventVerificationPassed   EventKind = "verification.passed"
	EventVerificationConflict EventKind = "verification.conflict"
	EventReferenceImported    EventKind = "reference.imported"
	EventCoverageSnapshot     EventKind = "coverage.snapshot"
)

// Moderation and administration.
const (
	EventModerationReport EventKind = "moderation.report"
	EventModerationAction EventKind = "moderation.action"
	EventModerationAppeal EventKind = "moderation.appeal"
	EventAdminAction      EventKind = "admin.action"
	EventAuthFailure      EventKind = "auth.failure"
	EventRateLimited      EventKind = "rate.limited"
)

// Account, Swarm, and invitation lifecycle. ADR 0016.
const (
	EventUserRegistered   EventKind = "user.registered"
	EventSwarmCreated     EventKind = "swarm.created"
	EventInvitationIssued EventKind = "invitation.issued"
	EventBridgeReenrolled EventKind = "bridge.reenrolled"
)

// eventSpec describes one member of the vocabulary.
type eventSpec struct {
	Retention Retention
	// NamesTitle records whether this event kind may carry an exact game or
	// file name. Kinds marked true are what the retention sweeper strips or
	// aggregates first.
	NamesTitle bool
	Summary    string
}

var vocabulary = map[EventKind]eventSpec{
	EventBridgeEnrolled: {RetentionAudit, false, "a Bridge completed enrolment against an invitation"},
	EventBridgeApproved: {RetentionAudit, false, "an owner or administrator approved a pending Bridge"},
	EventBridgeOnline:   {RetentionOperational, false, "a Bridge heartbeat resumed"},
	EventBridgeOffline:  {RetentionOperational, false, "a Bridge stopped heartbeating"},
	EventBridgeStale:    {RetentionOperational, false, "a Bridge has not revalidated inventory within the freshness window"},
	EventBridgeDisabled: {RetentionAudit, false, "a Bridge was disabled"},
	EventBridgeRevoked:  {RetentionAudit, false, "a Bridge identity was revoked"},

	EventTokenIssued:      {RetentionSecurity, false, "an access or refresh credential was issued"},
	EventTokenRotated:     {RetentionSecurity, false, "a refresh credential rotated normally"},
	EventTokenGraceUsed:   {RetentionSecurity, false, "the one-use previous-token grace path was taken after an interrupted rotation"},
	EventTokenReuseDetect: {RetentionSecurity, false, "a refresh token was replayed outside the grace path"},
	EventTokenFamilyRevkd: {RetentionSecurity, false, "a token family was revoked after reuse detection"},

	EventInventorySnapshot:  {RetentionOperational, true, "a Bridge published a full per-Swarm inventory snapshot"},
	EventInventoryDelta:     {RetentionOperational, true, "a Bridge published an incremental inventory change"},
	EventInventoryTombstone: {RetentionOperational, true, "a Bridge confirmed deletion of an item it previously offered"},
	EventInventoryReconcile: {RetentionOperational, false, "a full reconciliation pass ran"},
	EventInventoryDrift:     {RetentionOperational, false, "central and Bridge fingerprints disagreed"},

	EventAccessRequested: {RetentionOperational, true, "a member requested access to an item"},
	EventAccessApproved:  {RetentionOperational, true, "a source operator or policy approved a request"},
	EventAccessDenied:    {RetentionOperational, true, "a source operator or policy denied a request"},
	EventGrantIssued:     {RetentionOperational, true, "the Host issued a short-lived single-item grant"},
	EventGrantExpired:    {RetentionOperational, false, "a grant reached its expiry without being fully used"},
	EventGrantRevoked:    {RetentionSecurity, false, "a grant was revoked before expiry"},
	EventGrantReplay:     {RetentionSecurity, false, "a grant was presented after expiry or by the wrong audience"},

	EventTransferStarted:   {RetentionOperational, true, "a transfer began"},
	EventTransferResumed:   {RetentionOperational, true, "an interrupted transfer resumed from an offset"},
	EventTransferRouted:    {RetentionOperational, false, "a transfer selected or switched between the direct and relayed route"},
	EventTransferCompleted: {RetentionOperational, true, "a transfer completed and passed final verification"},
	EventTransferFailed:    {RetentionOperational, true, "a transfer failed"},
	EventTransferCancelled: {RetentionOperational, true, "a transfer was cancelled"},

	EventDestinationState: {RetentionOperational, true, "the receiving Bridge's destination state machine advanced"},

	EventVerificationPassed:   {RetentionOperational, true, "a payload matched its expected canonical and reference identity"},
	EventVerificationConflict: {RetentionSecurity, true, "a payload did not match its expected identity"},
	EventReferenceImported:    {RetentionAudit, false, "a reference catalogue version was imported"},
	EventCoverageSnapshot:     {RetentionAggregate, false, "a daily coverage and resilience snapshot was recorded"},

	EventModerationReport: {RetentionAudit, false, "a member reported content or behaviour"},
	EventModerationAction: {RetentionAudit, false, "a moderator acted, with actor, reason, evidence, time, duration, and outcome"},
	EventModerationAppeal: {RetentionAudit, false, "a member appealed a moderation action"},
	EventAdminAction:      {RetentionAudit, false, "an administrative change was made"},
	EventAuthFailure:      {RetentionSecurity, false, "an authentication attempt failed"},
	EventRateLimited:      {RetentionSecurity, false, "a caller exceeded a rate limit"},

	EventUserRegistered:   {RetentionAudit, false, "a Host account was created"},
	EventSwarmCreated:     {RetentionAudit, false, "a Swarm was created"},
	EventInvitationIssued: {RetentionAudit, false, "a Swarm invitation was issued"},
	EventBridgeReenrolled: {RetentionAudit, false, "an owner re-enrolled a revoked Bridge identity"},
}

// Known reports whether the kind is part of the vocabulary. The Host rejects
// unknown kinds rather than storing them, so a Bridge running ahead of the Host
// cannot quietly write events the Host's retention sweeper does not understand
// and therefore would never delete.
func (k EventKind) Known() bool {
	_, ok := vocabulary[k]
	return ok
}

// Retention returns the retention class for the kind.
func (k EventKind) Retention() (Retention, error) {
	spec, ok := vocabulary[k]
	if !ok {
		return "", fmt.Errorf("unknown event kind %q", k)
	}
	return spec.Retention, nil
}

// NamesTitle reports whether the kind may carry an exact title.
func (k EventKind) NamesTitle() bool {
	return vocabulary[k].NamesTitle
}

// Summary returns the human description of the kind.
func (k EventKind) Summary() string {
	return vocabulary[k].Summary
}

// AllEventKinds returns the vocabulary in sorted order, for documentation
// generation and for the vocabulary-coverage test.
func AllEventKinds() []EventKind {
	out := make([]EventKind, 0, len(vocabulary))
	for k := range vocabulary {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Event is the envelope every recorded event shares.
//
// Note what is absent: there is no free-form message field. A free-form field
// becomes the place where search terms, file paths, and IP addresses end up by
// accident, and it defeats the retention classification above. Detail belongs
// in typed fields on the specific payload.
type Event struct {
	Kind      EventKind `json:"kind"`
	At        time.Time `json:"at"`
	SwarmID   SwarmID   `json:"swarm_id,omitempty"`
	ActorUser UserID    `json:"actor_user,omitempty"`
	// ActorBridge is the global Bridge identity. It appears in Host-internal
	// and audit records only. Member-facing APIs project it to a BridgeAlias.
	ActorBridge BridgeID `json:"actor_bridge,omitempty"`
}

// Validate checks the envelope.
func (e Event) Validate() error {
	if !e.Kind.Known() {
		return fmt.Errorf("event: unknown kind %q", e.Kind)
	}
	if e.At.IsZero() {
		return fmt.Errorf("event %s: missing timestamp", e.Kind)
	}
	if e.ActorUser != "" {
		if err := e.ActorUser.Validate(); err != nil {
			return fmt.Errorf("event %s: %w", e.Kind, err)
		}
	}
	if e.ActorBridge != "" {
		if err := e.ActorBridge.Validate(); err != nil {
			return fmt.Errorf("event %s: %w", e.Kind, err)
		}
	}
	if e.SwarmID != "" {
		if err := e.SwarmID.Validate(); err != nil {
			return fmt.Errorf("event %s: %w", e.Kind, err)
		}
	}
	return nil
}
