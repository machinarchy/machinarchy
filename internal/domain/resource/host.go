package resource

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

const MaxConditions = 16

type DesiredPresence string

const (
	PresenceManaged DesiredPresence = "Managed"
	PresenceAbsent  DesiredPresence = "Absent"
)

type HostPhase string

const (
	HostPending  HostPhase = "Pending"
	HostActive   HostPhase = "Active"
	HostDeleting HostPhase = "Deleting"
)

type ConditionStatus string

const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

type Condition struct {
	Type               string          `json:"type"`
	Status             ConditionStatus `json:"status"`
	Reason             string          `json:"reason"`
	Message            string          `json:"message,omitempty"`
	ObservedGeneration uint64          `json:"observedGeneration"`
	LastTransitionTime time.Time       `json:"lastTransitionTime"`
}

type HostMetadata struct {
	ID                ID                `json:"id"`
	WorkspaceID       int               `json:"workspaceId"`
	EnvironmentID     int               `json:"environmentId,omitempty"`
	Name              string            `json:"name"`
	Labels            map[string]string `json:"labels,omitempty"`
	Generation        uint64            `json:"generation"`
	Revision          uint64            `json:"revision"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
}

type HostSpec struct {
	Presence        DesiredPresence `json:"presence"`
	AccessProfileID ID              `json:"accessProfileId,omitempty"`
	ProfileRevision uint64          `json:"profileRevision,omitempty"`
	// AccessTarget is host-owned connector-neutral location and trust material.
	// Its interpretation is restricted to the selected connector adapter.
	AccessTarget HostAccessTarget `json:"accessTarget,omitempty"`
}

type HostAccessTarget struct {
	Endpoint string   `json:"endpoint,omitempty"`
	Trust    []string `json:"trust,omitempty"`
}

type HostStatus struct {
	ObservedGeneration uint64      `json:"observedGeneration"`
	Phase              HostPhase   `json:"phase"`
	Conditions         []Condition `json:"conditions,omitempty"`
	LastObservedAt     *time.Time  `json:"lastObservedAt,omitempty"`
}

// Host is the connector-neutral managed resource. Connector credentials and
// protocol details live behind AccessProfileID and are never part of this type.
type Host struct {
	Metadata HostMetadata `json:"metadata"`
	Spec     HostSpec     `json:"spec"`
	Status   HostStatus   `json:"status"`
}

var (
	ErrHostNotFound      = errors.New("managed host not found")
	ErrOperationNotFound = errors.New("operation not found")
	ErrConflict          = errors.New("resource revision conflict")
	ErrInvalidTransition = errors.New("invalid resource lifecycle transition")
	ErrOperationFinished = errors.New("operation is already terminal")
	ErrInvalidResource   = errors.New("invalid resource")
)

func NewHost(workspaceID, environmentID int, name string, labels map[string]string, spec HostSpec, now time.Time) (*Host, error) {
	if spec.Presence == "" {
		spec.Presence = PresenceManaged
	}
	id, err := NewResourceID()
	if err != nil {
		return nil, err
	}
	host := &Host{
		Metadata: HostMetadata{ID: id, WorkspaceID: workspaceID, EnvironmentID: environmentID,
			Name: strings.TrimSpace(name), Labels: cloneLabels(labels), Generation: 1, Revision: 1,
			CreatedAt: now.UTC(), UpdatedAt: now.UTC()},
		Spec: spec,
		Status: HostStatus{Phase: HostPending, Conditions: []Condition{
			{Type: "Ready", Status: ConditionUnknown, Reason: "NotObserved", ObservedGeneration: 0, LastTransitionTime: now.UTC()},
			{Type: "Reachable", Status: ConditionUnknown, Reason: "NotObserved", ObservedGeneration: 0, LastTransitionTime: now.UTC()},
			{Type: "Configured", Status: ConditionUnknown, Reason: "NotObserved", ObservedGeneration: 0, LastTransitionTime: now.UTC()},
		}},
	}
	if err := host.Validate(); err != nil {
		return nil, err
	}
	return host, nil
}

func (h *Host) Validate() error {
	if h == nil || h.Metadata.ID.Validate() != nil || h.Metadata.WorkspaceID <= 0 ||
		h.Metadata.Generation == 0 || h.Metadata.Revision == 0 || h.Metadata.Name == "" || len(h.Metadata.Name) > 255 {
		return fmt.Errorf("%w: Host identity, workspace, name, generation, and revision are required", ErrInvalidResource)
	}
	if h.Metadata.EnvironmentID < 0 {
		return fmt.Errorf("%w: environment ID cannot be negative", ErrInvalidResource)
	}
	if err := domain.ValidateLabels(h.Metadata.Labels); err != nil {
		return err
	}
	if err := h.Spec.Validate(); err != nil {
		return err
	}
	if h.Status.Phase != HostPending && h.Status.Phase != HostActive && h.Status.Phase != HostDeleting {
		return fmt.Errorf("%w: unknown Host phase %q", ErrInvalidResource, h.Status.Phase)
	}
	if h.Status.ObservedGeneration > h.Metadata.Generation {
		return fmt.Errorf("%w: observed generation cannot exceed desired generation", ErrInvalidResource)
	}
	if err := ValidateConditions(h.Status.Conditions, h.Metadata.Generation); err != nil {
		return err
	}
	return nil
}

func (s HostSpec) Validate() error {
	if s.Presence == "" {
		s.Presence = PresenceManaged
	}
	if s.Presence != PresenceManaged && s.Presence != PresenceAbsent {
		return fmt.Errorf("%w: unknown desired presence %q", ErrInvalidResource, s.Presence)
	}
	if s.AccessProfileID != "" {
		if err := s.AccessProfileID.Validate(); err != nil {
			return fmt.Errorf("%w: access profile ID: %w", ErrInvalidResource, err)
		}
	}
	if len(s.AccessTarget.Endpoint) > 512 || len(s.AccessTarget.Trust) > 8 {
		return fmt.Errorf("%w: Host access target exceeds supported bounds", ErrInvalidResource)
	}
	for _, value := range s.AccessTarget.Trust {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return fmt.Errorf("%w: Host trust value is invalid", ErrInvalidResource)
		}
	}
	return nil
}

func (h *Host) UpdateSpec(expectedGeneration uint64, next HostSpec, now time.Time) error {
	if h == nil {
		return fmt.Errorf("%w: Host is required", ErrInvalidResource)
	}
	if h.Metadata.Generation != expectedGeneration {
		return ErrConflict
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if next.Presence == "" {
		next.Presence = PresenceManaged
	}
	if reflect.DeepEqual(h.Spec, next) {
		return nil
	}
	h.Spec = next
	h.Metadata.Generation++
	h.Metadata.Revision++
	h.Metadata.UpdatedAt = now.UTC()
	return h.Validate()
}

func (h *Host) UpdateMetadata(expectedRevision uint64, name string, environmentID int, labels map[string]string, now time.Time) error {
	if h == nil {
		return fmt.Errorf("%w: Host is required", ErrInvalidResource)
	}
	if h.Metadata.Revision != expectedRevision {
		return ErrConflict
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 || environmentID < 0 {
		return fmt.Errorf("%w: invalid Host metadata", ErrInvalidResource)
	}
	if err := domain.ValidateLabels(labels); err != nil {
		return err
	}
	h.Metadata.Name = name
	h.Metadata.EnvironmentID = environmentID
	h.Metadata.Labels = cloneLabels(labels)
	h.Metadata.Revision++
	h.Metadata.UpdatedAt = now.UTC()
	return h.Validate()
}

func (h *Host) Observe(expectedGeneration uint64, phase HostPhase, conditions []Condition, now time.Time) error {
	if h == nil {
		return fmt.Errorf("%w: Host is required", ErrInvalidResource)
	}
	if h.Metadata.Generation != expectedGeneration {
		return ErrConflict
	}
	if phase != HostPending && phase != HostActive && phase != HostDeleting {
		return fmt.Errorf("%w: unknown Host phase %q", ErrInvalidTransition, phase)
	}
	if h.Status.Phase == HostDeleting && phase != HostDeleting {
		return fmt.Errorf("%w: deleting Host cannot return to an active phase", ErrInvalidTransition)
	}
	normalized, err := NormalizeConditions(h.Status.Conditions, conditions, expectedGeneration, now)
	if err != nil {
		return err
	}
	observed := now.UTC()
	h.Status.Phase = phase
	h.Status.ObservedGeneration = expectedGeneration
	h.Status.Conditions = normalized
	h.Status.LastObservedAt = &observed
	h.Metadata.Revision++
	h.Metadata.UpdatedAt = observed
	return h.Validate()
}

func (h *Host) RequestDeletion(expectedGeneration uint64, now time.Time) error {
	if h == nil {
		return fmt.Errorf("%w: Host is required", ErrInvalidResource)
	}
	return h.UpdateSpec(expectedGeneration, HostSpec{Presence: PresenceAbsent, AccessProfileID: h.Spec.AccessProfileID,
		ProfileRevision: h.Spec.ProfileRevision, AccessTarget: h.Spec.AccessTarget}, now)
}

func NormalizeConditions(previous, next []Condition, generation uint64, now time.Time) ([]Condition, error) {
	if err := ValidateConditions(next, generation); err != nil {
		return nil, err
	}
	old := make(map[string]Condition, len(previous))
	for _, condition := range previous {
		old[condition.Type] = condition
	}
	out := make([]Condition, len(next))
	for i, condition := range next {
		condition.Message = sanitizeMessage(condition.Message, 1024)
		condition.ObservedGeneration = generation
		if prior, ok := old[condition.Type]; ok && prior.Status == condition.Status && prior.Reason == condition.Reason {
			condition.LastTransitionTime = prior.LastTransitionTime
		} else if condition.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = now.UTC()
		} else {
			condition.LastTransitionTime = condition.LastTransitionTime.UTC()
		}
		out[i] = condition
	}
	return out, nil
}

func ValidateConditions(conditions []Condition, generation uint64) error {
	if len(conditions) > MaxConditions {
		return fmt.Errorf("%w: at most %d conditions are allowed", ErrInvalidResource, MaxConditions)
	}
	seen := make(map[string]struct{}, len(conditions))
	for _, condition := range conditions {
		if condition.Type == "" || len(condition.Type) > 64 || condition.Reason == "" || len(condition.Reason) > 128 {
			return fmt.Errorf("%w: condition type and reason must be bounded and nonempty", ErrInvalidResource)
		}
		if condition.Status != ConditionTrue && condition.Status != ConditionFalse && condition.Status != ConditionUnknown {
			return fmt.Errorf("%w: invalid condition status %q", ErrInvalidResource, condition.Status)
		}
		if len(condition.Message) > 4096 || condition.ObservedGeneration > generation {
			return fmt.Errorf("%w: condition message or observed generation is invalid", ErrInvalidResource)
		}
		if _, exists := seen[condition.Type]; exists {
			return fmt.Errorf("%w: duplicate condition type %q", ErrInvalidResource, condition.Type)
		}
		seen[condition.Type] = struct{}{}
	}
	return nil
}

func cloneLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	clone := make(map[string]string, len(labels))
	for key, value := range labels {
		clone[key] = value
	}
	return clone
}

func sanitizeMessage(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	if len(value) > limit {
		value = value[:limit]
	}
	return strings.TrimSpace(value)
}
