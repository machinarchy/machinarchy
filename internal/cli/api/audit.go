package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// AuditClient reads the workspace audit trail. Every call needs an owner
// signed in with a password; the server refuses API tokens.
type AuditClient interface {
	// ListAuditEvents returns one page of events, newest first. beforeID 0
	// means from the newest.
	ListAuditEvents(ctx context.Context, action string, beforeID int64, limit int) ([]AuditEvent, error)
	VerifyAudit(ctx context.Context) (*AuditVerification, error)
	// PruneAudit removes the events below req.BeforeID once the server has
	// checked the archive proof; it needs an owner session.
	PruneAudit(ctx context.Context, req AuditPruneRequest) (*AuditCheckpoint, error)
}

var _ AuditClient = (*HTTPClient)(nil)

// AuditEvent is one append-only audit record. It never holds a credential.
type AuditEvent struct {
	ID            int64             `json:"id" yaml:"id"`
	OccurredAt    time.Time         `json:"occurredAt" yaml:"occurredAt"`
	ActorUserID   int               `json:"actorUserId,omitempty" yaml:"actorUserId,omitempty"`
	ActorUsername string            `json:"actorUsername,omitempty" yaml:"actorUsername,omitempty"`
	ActorTokenID  int               `json:"actorTokenId,omitempty" yaml:"actorTokenId,omitempty"`
	RequestID     string            `json:"requestId,omitempty" yaml:"requestId,omitempty"`
	Source        string            `json:"source,omitempty" yaml:"source,omitempty"`
	Action        string            `json:"action" yaml:"action"`
	TargetType    string            `json:"targetType" yaml:"targetType"`
	TargetID      string            `json:"targetId,omitempty" yaml:"targetId,omitempty"`
	Outcome       string            `json:"outcome" yaml:"outcome"`
	Details       map[string]string `json:"details" yaml:"details"`
	PrevHash      string            `json:"prevHash" yaml:"prevHash"`
	Hash          string            `json:"hash" yaml:"hash"`
}

// AuditVerification is the server's walk of the hash chain.
type AuditVerification struct {
	Valid          bool   `json:"valid" yaml:"valid"`
	Events         int    `json:"events" yaml:"events"`
	HeadHash       string `json:"headHash" yaml:"headHash"`
	FirstInvalidID int64  `json:"firstInvalidId,omitempty" yaml:"firstInvalidId,omitempty"`
	Reason         string `json:"reason,omitempty" yaml:"reason,omitempty"`
	// Checkpoint is set when older events were pruned: the walk starts there.
	Checkpoint *AuditCheckpoint `json:"checkpoint,omitempty" yaml:"checkpoint,omitempty"`
}

// AuditCheckpoint records one prune: the newest removed event and its hash.
type AuditCheckpoint struct {
	ID            int64     `json:"id" yaml:"id"`
	LastEventID   int64     `json:"lastEventId" yaml:"lastEventId"`
	LastHash      string    `json:"lastHash" yaml:"lastHash"`
	EventsRemoved int       `json:"eventsRemoved" yaml:"eventsRemoved"`
	ActorUserID   int       `json:"actorUserId,omitempty" yaml:"actorUserId,omitempty"`
	CreatedAt     time.Time `json:"createdAt" yaml:"createdAt"`
}

// AuditPruneRequest names the range to remove and proves an archive of it.
type AuditPruneRequest struct {
	BeforeID        int64  `json:"beforeId"`
	ArchiveHeadHash string `json:"archiveHeadHash"`
	ArchiveCount    int    `json:"archiveCount"`
}

func (c *HTTPClient) ListAuditEvents(ctx context.Context, action string, beforeID int64, limit int) ([]AuditEvent, error) {
	query := url.Values{}
	if action != "" {
		query.Set("action", action)
	}
	if beforeID > 0 {
		query.Set("before_id", strconv.FormatInt(beforeID, 10))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	endpoint := "/api/v1/audit/events"
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var events []AuditEvent
	err := c.get(ctx, endpoint, &events)

	return events, err
}

func (c *HTTPClient) VerifyAudit(ctx context.Context) (*AuditVerification, error) {
	var result AuditVerification
	if err := c.get(ctx, "/api/v1/audit/verify", &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *HTTPClient) PruneAudit(ctx context.Context, req AuditPruneRequest) (*AuditCheckpoint, error) {
	var checkpoint AuditCheckpoint
	if err := c.request(ctx, http.MethodPost, "/api/v1/audit/prune", req, &checkpoint); err != nil {
		return nil, err
	}

	return &checkpoint, nil
}
