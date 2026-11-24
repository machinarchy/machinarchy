package resource

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// Paging bounds for list queries. Lists use keyset pagination: a page holds at
// most Limit items and NextCursor resumes after the last one.
const (
	DefaultPageSize = 100
	MaxPageSize     = 500
)

// PageLimit clamps a requested page size.
func PageLimit(limit int) int {
	if limit <= 0 {
		return DefaultPageSize
	}
	if limit > MaxPageSize {
		return MaxPageSize
	}
	return limit
}

// HostListFilter selects managed Hosts in one workspace, ordered by ID.
// Zero values mean "no filter".
type HostListFilter struct {
	WorkspaceID   int
	EnvironmentID int
	Phase         HostPhase
	After         ID
	Limit         int
	// Labels narrows the page to Hosts that satisfy every constraint,
	// evaluated in the database (see domain.Selector.LabelConstraints).
	Labels []domain.LabelConstraint
	// Lookup, when not empty, keeps only Hosts whose ID or name (ignoring
	// case) is in it.
	Lookup []string
}

// OperationListFilter selects Operations in one workspace, newest first.
type OperationListFilter struct {
	WorkspaceID   int
	State         OperationState
	HostID        ID
	CreatedAfter  time.Time
	CreatedBefore time.Time
	After         string // opaque cursor from OperationSummary.Cursor
	Limit         int
}

// OperationSummary is the list view of an Operation. It never contains argv
// or output, which stay behind GetOperation and its permission checks.
type OperationSummary struct {
	ID          ID                  `json:"id"`
	Kind        OperationKind       `json:"kind"`
	State       OperationState      `json:"state"`
	CreatedBy   ActorRef            `json:"createdBy"`
	CancelAsked bool                `json:"cancelRequested"`
	CreatedAt   time.Time           `json:"createdAt"`
	StartedAt   *time.Time          `json:"startedAt,omitempty"`
	FinishedAt  *time.Time          `json:"finishedAt,omitempty"`
	Targets     map[TargetState]int `json:"targets"`
	// Cursor resumes a listing after this Operation.
	Cursor string `json:"-"`
}

// OperationCursor encodes the stored sort key of an Operation. The created_at
// text is kept verbatim so the cursor compares exactly like the stored rows.
func OperationCursor(createdAt string, id ID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + string(id)))
}

// ParseOperationCursor reverses OperationCursor.
func ParseOperationCursor(cursor string) (createdAt string, id ID, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid cursor", ErrInvalidResource)
	}
	created, rawID, ok := strings.Cut(string(raw), "\x00")
	if !ok || created == "" || ID(rawID).Validate() != nil {
		return "", "", fmt.Errorf("%w: invalid cursor", ErrInvalidResource)
	}
	return created, ID(rawID), nil
}

// Page is one page of a list response.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}
