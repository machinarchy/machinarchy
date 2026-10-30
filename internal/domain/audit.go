package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Audit actions. Details never contain credentials; see SanitizeAuditDetails.
const (
	AuditUserCreated       = "user.created"
	AuditMemberRoleChanged = "member.role_changed"
	AuditHostCreated       = "host.created"
	AuditHostUpdated       = "host.updated"
	AuditHostDeleted       = "host.deleted"
	AuditHostLabelsPatched = "host.labels_patched"
	AuditAPITokenCreated   = "api_token.created"
	AuditAPITokenRevoked   = "api_token.revoked"

	AuditTargetUser     = "user"
	AuditTargetHost     = "host"
	AuditTargetAPIToken = "api_token"

	AuditOutcomeSuccess = "success"

	// AuditGenesisHash is the previous hash of a workspace's first event.
	AuditGenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

	maxAuditDetails     = 20
	maxAuditDetailValue = 256

	// DefaultAuditPageSize and MaxAuditPageSize bound event listings.
	DefaultAuditPageSize = 100
	MaxAuditPageSize     = 500
)

var auditDetailKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// AuditActor identifies who caused a change. The zero value means the system
// itself (for example a background or maintenance path with no caller).
type AuditActor struct {
	UserID     int
	Username   string
	APITokenID int
	RequestID  string
	// Source names the entry point, such as "api", "signup", or "bootstrap".
	Source string
}

type auditActorKey struct{}

// WithAuditActor attaches the actor to ctx. Stores read it when they record the
// audit event for a mutation, in the same transaction as the mutation.
func WithAuditActor(ctx context.Context, actor AuditActor) context.Context {
	return context.WithValue(ctx, auditActorKey{}, actor)
}

// AuditActorFrom returns the actor attached to ctx, if any.
func AuditActorFrom(ctx context.Context) (AuditActor, bool) {
	actor, ok := ctx.Value(auditActorKey{}).(AuditActor)
	return actor, ok
}

// AuditEvent is one append-only record. Events of a workspace form a hash
// chain: each event's Hash covers its own fields and the previous event's Hash,
// so editing, removing, or reordering an earlier event is detectable.
type AuditEvent struct {
	ID            int64             `json:"id"`
	WorkspaceID   int               `json:"-"`
	OccurredAt    time.Time         `json:"occurredAt"`
	ActorUserID   int               `json:"actorUserId,omitempty"`
	ActorUsername string            `json:"actorUsername,omitempty"`
	ActorTokenID  int               `json:"actorTokenId,omitempty"`
	RequestID     string            `json:"requestId,omitempty"`
	Source        string            `json:"source,omitempty"`
	Action        string            `json:"action"`
	TargetType    string            `json:"targetType"`
	TargetID      string            `json:"targetId,omitempty"`
	Outcome       string            `json:"outcome"`
	Details       map[string]string `json:"details"`
	PrevHash      string            `json:"prevHash"`
	Hash          string            `json:"hash"`
}

// StoredAuditEvent is an event as read back from storage, with the exact
// timestamp text that was hashed.
type StoredAuditEvent struct {
	AuditEvent
	OccurredAtText string
}

// AuditFilter selects and pages events, newest first.
type AuditFilter struct {
	Action string
	// BeforeID returns only events with a smaller ID, for paging; 0 means no limit.
	BeforeID int64
	Limit    int
}

// AuditVerification is the result of walking a workspace's hash chain.
type AuditVerification struct {
	Valid  bool `json:"valid"`
	Events int  `json:"events"`
	// HeadHash is the hash of the newest event. Record it somewhere outside the
	// database (a ticket, a log line) to detect later truncation of the tail.
	HeadHash       string `json:"headHash"`
	FirstInvalidID int64  `json:"firstInvalidId,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// AuditStore reads audit events. Writing happens inside the stores' own
// transactions so an event can never exist without its change, or the reverse.
type AuditStore interface {
	ListAuditEvents(ctx context.Context, workspaceID int, filter AuditFilter) ([]AuditEvent, error)
	VerifyAuditChain(ctx context.Context, workspaceID int) (*AuditVerification, error)
}

// AuditService exposes the audit trail to workspace owners.
type AuditService interface {
	List(ctx context.Context, principal Principal, filter AuditFilter) ([]AuditEvent, error)
	Verify(ctx context.Context, principal Principal) (*AuditVerification, error)
}

// NormalizeAuditFilter applies defaults and bounds to a listing request.
func NormalizeAuditFilter(filter AuditFilter) (AuditFilter, error) {
	switch {
	case filter.Limit < 0 || filter.BeforeID < 0:
		return filter, fmt.Errorf("%w: limit and before_id must not be negative", ErrInvalidInput)
	case filter.Limit == 0:
		filter.Limit = DefaultAuditPageSize
	case filter.Limit > MaxAuditPageSize:
		filter.Limit = MaxAuditPageSize
	}
	filter.Action = strings.TrimSpace(filter.Action)
	if len(filter.Action) > 64 {
		return filter, fmt.Errorf("%w: action filter is too long", ErrInvalidInput)
	}
	return filter, nil
}

// SanitizeAuditDetails returns a safe copy of details: bounded in size, with
// well-formed keys, control characters removed, and long values truncated. It
// is a safety net; callers must still never put secrets in details.
func SanitizeAuditDetails(details map[string]string) (map[string]string, error) {
	if len(details) > maxAuditDetails {
		return nil, fmt.Errorf("%w: an audit event may carry at most %d details", ErrInvalidInput, maxAuditDetails)
	}
	clean := make(map[string]string, len(details))
	for key, value := range details {
		if !auditDetailKey.MatchString(key) {
			return nil, fmt.Errorf("%w: invalid audit detail key %q", ErrInvalidInput, truncateForError(key))
		}
		value = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, value)
		if runes := []rune(value); len(runes) > maxAuditDetailValue {
			value = string(runes[:maxAuditDetailValue-1]) + "…"
		}
		clean[key] = value
	}
	return clean, nil
}

// ComputeAuditHash returns the chain hash of e given the previous event's hash
// and the exact timestamp text that is stored. Every field is length-prefixed
// so no two different events can produce the same input.
func ComputeAuditHash(prevHash, occurredAt string, e AuditEvent) string {
	digest := sha256.New()
	field := func(value string) { fmt.Fprintf(digest, "%d:%s|", len(value), value) }
	field(prevHash)
	field(fmt.Sprint(e.WorkspaceID))
	field(occurredAt)
	field(fmt.Sprint(e.ActorUserID))
	field(e.ActorUsername)
	field(fmt.Sprint(e.ActorTokenID))
	field(e.RequestID)
	field(e.Source)
	field(e.Action)
	field(e.TargetType)
	field(e.TargetID)
	field(e.Outcome)
	keys := make([]string, 0, len(e.Details))
	for key := range e.Details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	field(fmt.Sprint(len(keys)))
	for _, key := range keys {
		field(key)
		field(e.Details[key])
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// AuditChainVerifier checks a workspace's events in ascending ID order.
type AuditChainVerifier struct {
	prev   string
	result AuditVerification
	failed bool
}

// NewAuditChainVerifier starts at the genesis hash.
func NewAuditChainVerifier() *AuditChainVerifier {
	return &AuditChainVerifier{prev: AuditGenesisHash, result: AuditVerification{Valid: true, HeadHash: AuditGenesisHash}}
}

// Add checks the next event and reports whether the chain is still intact.
// After the first failure it ignores further events.
func (v *AuditChainVerifier) Add(stored StoredAuditEvent) bool {
	if v.failed {
		return false
	}
	fail := func(reason string) bool {
		v.failed = true
		v.result.Valid = false
		v.result.FirstInvalidID = stored.ID
		v.result.Reason = reason
		return false
	}
	if stored.PrevHash != v.prev {
		return fail("the event does not link to the previous event (an event was removed, inserted, or reordered)")
	}
	if ComputeAuditHash(stored.PrevHash, stored.OccurredAtText, stored.AuditEvent) != stored.Hash {
		return fail("the event's contents do not match its hash (the event was modified)")
	}
	v.prev = stored.Hash
	v.result.Events++
	v.result.HeadHash = stored.Hash
	return true
}

// Result returns the verification outcome so far.
func (v *AuditChainVerifier) Result() AuditVerification { return v.result }
