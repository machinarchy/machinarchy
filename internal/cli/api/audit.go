package api

import (
	"context"
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
