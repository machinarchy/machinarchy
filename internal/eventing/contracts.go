// Package eventing defines versioned event and command contracts for the
// control plane. It contains no transport or persistence implementation.
package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

const (
	ConsumerControlManager  = "control-manager"
	ConsumerOperationWorker = "operation-worker"
	MaxEventPayloadBytes    = 64 << 10
	AccessProfileChanged    = "machinarchy.access-profile.changed"
	OperationCreated        = "machinarchy.operation.created"
)

type Subject struct {
	Kind       string      `json:"kind"`
	ID         resource.ID `json:"id"`
	Generation uint64      `json:"generation"`
	Revision   uint64      `json:"revision"`
}

type Actor struct {
	UserID     int `json:"userId,omitempty"`
	APITokenID int `json:"apiTokenId,omitempty"`
}

// Envelope is an immutable integration fact. Data contains a typed payload for
// Type and SchemaVersion; resource notifications must not embed snapshots.
type Envelope struct {
	ID            resource.ID     `json:"id"`
	Type          string          `json:"type"`
	SchemaVersion uint16          `json:"schemaVersion"`
	OccurredAt    time.Time       `json:"occurredAt"`
	Producer      string          `json:"producer"`
	WorkspaceID   int             `json:"workspaceId"`
	Subject       Subject         `json:"subject"`
	CorrelationID string          `json:"correlationId"`
	CausationID   string          `json:"causationId"`
	Actor         *Actor          `json:"actor,omitempty"`
	Data          json.RawMessage `json:"data"`
}

type HostChange struct {
	Change string `json:"change"`
}
type AccessProfileChange struct {
	Revision uint64 `json:"revision"`
}
type OperationChange struct {
	Revision uint64 `json:"revision"`
}

const (
	HostCreated           = "machinarchy.host.created"
	HostSpecChanged       = "machinarchy.host.spec-changed"
	HostDeletionRequested = "machinarchy.host.deletion-requested"
	HostDeleted           = "machinarchy.host.deleted"
	ConfigurationChanged  = "machinarchy.runtime-config.changed"
)

type RuntimeConfigChange struct {
	Revision uint64 `json:"revision"`
}

func NewHostEvent(eventType string, workspaceID int, subject Subject, change string, now time.Time, correlationID, causationID string, actor *Actor) (Envelope, error) {
	id, err := resource.NewResourceID()
	if err != nil {
		return Envelope{}, err
	}
	data, err := json.Marshal(HostChange{Change: change})
	if err != nil {
		return Envelope{}, err
	}
	event := Envelope{ID: id, Type: eventType, SchemaVersion: 1, OccurredAt: now.UTC(), Producer: "machinarchy.persistence",
		WorkspaceID: workspaceID, Subject: subject, CorrelationID: correlationID, CausationID: causationID, Actor: actor, Data: data}
	if err := event.Validate(); err != nil {
		return Envelope{}, err
	}
	return event, nil
}

func (e Envelope) Validate() error {
	if e.ID.Validate() != nil || e.WorkspaceID <= 0 || e.Subject.ID.Validate() != nil ||
		e.Subject.Generation == 0 || e.Subject.Revision == 0 || e.SchemaVersion == 0 || e.OccurredAt.IsZero() ||
		strings.TrimSpace(e.Producer) == "" || strings.TrimSpace(e.Type) == "" || len(e.Type) > 128 || len(e.Data) == 0 || len(e.Data) > MaxEventPayloadBytes {
		return errors.New("event envelope is missing required fields or exceeds bounds")
	}
	switch e.Type {
	case HostCreated, HostSpecChanged, HostDeletionRequested, HostDeleted:
		if e.Subject.Kind != "host" {
			return errors.New("host event subject kind is invalid")
		}
		var payload HostChange
		if err := json.Unmarshal(e.Data, &payload); err != nil || payload.Change == "" || len(payload.Change) > 64 {
			return errors.New("event payload is invalid")
		}
	case ConfigurationChanged:
		if e.Subject.Kind != "runtime-config" {
			return errors.New("runtime configuration event subject kind is invalid")
		}
		var payload RuntimeConfigChange
		if err := json.Unmarshal(e.Data, &payload); err != nil || payload.Revision == 0 || payload.Revision != e.Subject.Revision {
			return errors.New("runtime configuration event payload is invalid")
		}
	case AccessProfileChanged:
		if e.Subject.Kind != "access-profile" {
			return errors.New("access profile event subject kind is invalid")
		}
		var payload AccessProfileChange
		if err := json.Unmarshal(e.Data, &payload); err != nil || payload.Revision == 0 || payload.Revision != e.Subject.Revision {
			return errors.New("access profile event payload is invalid")
		}
	case OperationCreated:
		if e.Subject.Kind != "operation" {
			return errors.New("operation event subject kind is invalid")
		}
		var payload OperationChange
		if err := json.Unmarshal(e.Data, &payload); err != nil || payload.Revision == 0 || payload.Revision != e.Subject.Revision {
			return errors.New("operation event payload is invalid")
		}
	default:
		return fmt.Errorf("unsupported Host event type %q", e.Type)
	}
	if e.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema version %d for %s", e.SchemaVersion, e.Type)
	}
	if len(e.CorrelationID) > 128 || len(e.CausationID) > 128 {
		return errors.New("event correlation identifiers exceed bounds")
	}
	return nil
}

// CommandMetadata gives application commands stable identity without treating
// a command as a persisted fact or a transport message.
type CommandMetadata struct {
	ID            string
	CorrelationID string
	CausationID   string
}

type Command interface {
	CommandName() string
	Metadata() CommandMetadata
	Validate() error
}

type HostSpecUpdateCommand struct {
	Command            CommandMetadata
	WorkspaceID        int
	HostID             resource.ID
	ExpectedRevision   uint64
	ExpectedGeneration uint64
	Spec               resource.HostSpec
}

func (HostSpecUpdateCommand) CommandName() string         { return "host.spec-update" }
func (c HostSpecUpdateCommand) Metadata() CommandMetadata { return c.Command }

func (c HostSpecUpdateCommand) Validate() error {
	if c.Command.ID == "" || len(c.Command.ID) > 128 || len(c.Command.CorrelationID) > 128 || len(c.Command.CausationID) > 128 ||
		c.WorkspaceID <= 0 || c.HostID.Validate() != nil || c.ExpectedRevision == 0 || c.ExpectedGeneration == 0 {
		return errors.New("host spec update command requires workspace, resource identity, and concurrency preconditions")
	}
	return c.Spec.Validate()
}

// OutboxStore is the small durable-delivery port used by local dispatchers.
// Claim is at-least-once: expired claims may be returned again after a crash.
type OutboxStore interface {
	Claim(context.Context, string, string, time.Time, time.Duration, int) ([]ClaimedEvent, error)
	Acknowledge(context.Context, string, string, resource.ID, time.Time) error
	Retry(context.Context, string, string, resource.ID, time.Time, string) error
	Quarantine(context.Context, string, string, resource.ID, time.Time, string) error
	Prune(context.Context, time.Time) (int64, error)
}

type ClaimedEvent struct {
	Envelope Envelope
	Attempt  int
}
