package resource

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type ConnectorKind string
type AuthenticationMethod string

const (
	ConnectorSSH             ConnectorKind        = "ssh"
	AuthenticationPassword   AuthenticationMethod = "password"
	AuthenticationPrivateKey AuthenticationMethod = "private-key"
)

type SecretReference struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

type AccessProfileSpec struct {
	Connector      ConnectorKind        `json:"connectorKind"`
	Username       string               `json:"username"`
	Authentication AuthenticationMethod `json:"authenticationMethod"`
	Credential     SecretReference      `json:"credential"`
	Enabled        bool                 `json:"enabled"`
}

type AccessProfileRevision struct {
	ProfileID   ID                `json:"profileId"`
	WorkspaceID int               `json:"workspaceId"`
	Revision    uint64            `json:"revision"`
	Spec        AccessProfileSpec `json:"spec"`
	Checksum    string            `json:"checksum"`
	CreatedAt   time.Time         `json:"createdAt"`
	CreatedBy   int               `json:"createdBy"`
}

type AccessProfile struct {
	ID              ID         `json:"id"`
	WorkspaceID     int        `json:"workspaceId"`
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	CurrentRevision uint64     `json:"currentRevision"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	CreatedBy       int        `json:"createdBy"`
	DeletedAt       *time.Time `json:"deletedAt,omitempty"`
}

var ErrAccessProfileNotFound = errors.New("access profile not found")

func NewAccessProfile(workspaceID int, name, description string, spec AccessProfileSpec, actor int, now time.Time) (*AccessProfile, AccessProfileRevision, error) {
	id, err := NewResourceID()
	if err != nil {
		return nil, AccessProfileRevision{}, err
	}
	profile := &AccessProfile{ID: id, WorkspaceID: workspaceID, Name: strings.TrimSpace(name), Description: strings.TrimSpace(description), CurrentRevision: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), CreatedBy: actor}
	revision := AccessProfileRevision{ProfileID: id, WorkspaceID: workspaceID, Revision: 1, Spec: spec, CreatedAt: now.UTC(), CreatedBy: actor}
	if err := profile.Validate(); err != nil {
		return nil, AccessProfileRevision{}, err
	}
	if err := revision.Validate(); err != nil {
		return nil, AccessProfileRevision{}, err
	}
	return profile, revision, nil
}

func (p AccessProfile) Validate() error {
	if p.ID.Validate() != nil || p.WorkspaceID <= 0 || p.CurrentRevision == 0 || strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 || len(p.Description) > 512 || p.CreatedBy <= 0 {
		return fmt.Errorf("%w: invalid Access Profile metadata", ErrInvalidResource)
	}
	return nil
}
func (r AccessProfileRevision) Validate() error {
	if r.ProfileID.Validate() != nil || r.WorkspaceID <= 0 || r.Revision == 0 || r.CreatedBy <= 0 || r.CreatedAt.IsZero() || r.Spec.Connector != ConnectorSSH || strings.TrimSpace(r.Spec.Username) == "" || len(r.Spec.Username) > 255 || r.Spec.Credential.ID == "" || r.Spec.Credential.Version == 0 {
		return fmt.Errorf("%w: invalid Access Profile revision", ErrInvalidResource)
	}
	if r.Spec.Authentication != AuthenticationPassword && r.Spec.Authentication != AuthenticationPrivateKey {
		return fmt.Errorf("%w: unsupported Access Profile authentication method", ErrInvalidResource)
	}
	if len(r.Checksum) > 0 && len(r.Checksum) != 64 {
		return fmt.Errorf("%w: invalid Access Profile checksum", ErrInvalidResource)
	}
	return nil
}

func (p *AccessProfile) NextRevision(expected uint64, spec AccessProfileSpec, actor int, now time.Time) (AccessProfileRevision, error) {
	if p == nil || p.CurrentRevision != expected {
		return AccessProfileRevision{}, ErrConflict
	}
	r := AccessProfileRevision{ProfileID: p.ID, WorkspaceID: p.WorkspaceID, Revision: expected + 1, Spec: spec, CreatedAt: now.UTC(), CreatedBy: actor}
	if err := r.Validate(); err != nil {
		return AccessProfileRevision{}, err
	}
	p.CurrentRevision = r.Revision
	p.UpdatedAt = now.UTC()
	return r, nil
}
