// Package secrets provides write-only secret management and purpose-bound
// runtime resolution. Plaintext never appears in metadata or event contracts.
package secrets

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

var (
	ErrNotFound       = errors.New("secret not found")
	ErrUnauthorized   = errors.New("secret access denied")
	ErrInvalid        = errors.New("invalid secret request")
	ErrKeyUnavailable = errors.New("secret encryption key unavailable")
	ErrConflict       = errors.New("secret version conflict")
)

type Ref struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}
type Metadata struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	Purpose        Purpose    `json:"purpose"`
	CurrentVersion uint64     `json:"currentVersion"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	RetiredAt      *time.Time `json:"retiredAt,omitempty"`
}
type Version struct {
	Ref       Ref        `json:"ref"`
	CreatedAt time.Time  `json:"createdAt"`
	CreatedBy int        `json:"createdBy,omitempty"`
	RetiredAt *time.Time `json:"retiredAt,omitempty"`
}
type Input struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Purpose     Purpose `json:"purpose"`
	Value       string  `json:"value"`
}
type Ciphertext struct {
	KeyID        string
	AADVersion   int
	DataAADKeyID string
	WrappedDEK   []byte
	Value        []byte
}
type StoredVersion struct {
	Version
	Ciphertext Ciphertext
}
type RewrapCandidate struct {
	Ref        Ref
	Purpose    Purpose
	Ciphertext Ciphertext
}
type RewrapResult struct {
	KeyID     string `json:"keyId"`
	Rewrapped int    `json:"rewrapped"`
	Remaining int64  `json:"remaining"`
}

type Repository interface {
	CreateSecret(context.Context, Metadata, StoredVersion) error
	AppendSecretVersion(context.Context, string, uint64, StoredVersion) error
	GetSecret(context.Context, string) (Metadata, error)
	ListSecrets(context.Context, int) ([]Metadata, error)
	GetSecretVersion(context.Context, Ref) (StoredVersion, error)
	ListSecretVersions(context.Context, string) ([]Version, error)
	RetireSecret(context.Context, string, int) error
	ListRewrapCandidates(context.Context, string, int) ([]RewrapCandidate, error)
	RewrapDEK(context.Context, Ref, string, string, []byte, int) error
	CountVersionsByKey(context.Context, string) (int64, error)
}
type KeyProvider interface {
	Current(context.Context) (string, []byte, error)
	ByID(context.Context, string) ([]byte, error)
}
type WorkloadID string

const (
	WorkloadControlManager  WorkloadID = "control-manager"
	WorkloadOperationWorker WorkloadID = "operation-worker"
)

// ResolutionRequest binds a secret lookup to its authenticated workload and
// the exact resource snapshot that authorized the reference.
type ResolutionRequest struct {
	WorkloadID            WorkloadID
	Ref                   Ref
	Purpose               Purpose
	WorkspaceID           int
	AccessProfileID       string
	AccessProfileRevision uint64
	HostID                string
	OperationID           string
}

type ResolutionAuthorizer interface {
	AuthorizeSecretResolution(context.Context, ResolutionRequest) error
}

type DenyAllResolutionAuthorizer struct{}

func (DenyAllResolutionAuthorizer) AuthorizeSecretResolution(context.Context, ResolutionRequest) error {
	return ErrUnauthorized
}

type Purpose string

const (
	PurposeSSHCredential Purpose = "ssh-credential" // #nosec G101 -- purpose identifier, not credential material
	PurposeOperation     Purpose = "operation"
	PurposeConfiguration Purpose = "runtime-configuration"
)

type Service struct {
	repo       Repository
	keys       KeyProvider
	authorizer ResolutionAuthorizer
	random     io.Reader
}

func New(repo Repository, keys KeyProvider, authorizer ResolutionAuthorizer) (*Service, error) {
	if repo == nil || keys == nil || authorizer == nil {
		return nil, errors.New("secret store requires repository, external key provider, and resolution authorizer")
	}
	return &Service{repo: repo, keys: keys, authorizer: authorizer, random: rand.Reader}, nil
}

func (s *Service) Create(ctx context.Context, actor domain.Principal, input Input) (Metadata, error) {
	if err := authorize(actor); err != nil {
		return Metadata{}, err
	}
	if err := validateInput(input); err != nil {
		return Metadata{}, err
	}
	idValue, err := resource.NewResourceID()
	if err != nil {
		return Metadata{}, err
	}
	id := string(idValue)
	now := time.Now().UTC()
	meta := Metadata{ID: id, Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description), Purpose: input.Purpose, CurrentVersion: 1, CreatedAt: now, UpdatedAt: now}
	stored, err := s.encrypt(ctx, meta.ID, 1, actor.UserID, input.Value, input.Purpose, now)
	if err != nil {
		return Metadata{}, err
	}
	if err = s.repo.CreateSecret(ctx, meta, stored); err != nil {
		return Metadata{}, err
	}
	return meta, nil
}
func (s *Service) Rotate(ctx context.Context, actor domain.Principal, id string, expected uint64, input Input) (Metadata, error) {
	if err := authorize(actor); err != nil {
		return Metadata{}, err
	}
	meta, err := s.repo.GetSecret(ctx, id)
	if err != nil {
		return Metadata{}, err
	}
	if meta.RetiredAt != nil {
		return Metadata{}, ErrNotFound
	}
	if expected != meta.CurrentVersion {
		return Metadata{}, ErrConflict
	}
	input.Purpose = meta.Purpose
	if err := validateInput(input); err != nil {
		return Metadata{}, err
	}
	now := time.Now().UTC()
	stored, err := s.encrypt(ctx, id, expected+1, actor.UserID, input.Value, meta.Purpose, now)
	if err != nil {
		return Metadata{}, err
	}
	if err = s.repo.AppendSecretVersion(ctx, id, expected, stored); err != nil {
		return Metadata{}, err
	}
	// The returned value is re-read so the DB remains authoritative if a
	// concurrent rotation won.
	return s.repo.GetSecret(ctx, id)
}
func (s *Service) Describe(ctx context.Context, actor domain.Principal, id string) (Metadata, error) {
	if err := authorize(actor); err != nil {
		return Metadata{}, err
	}
	return s.repo.GetSecret(ctx, id)
}
func (s *Service) List(ctx context.Context, actor domain.Principal, limit int) ([]Metadata, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	return s.repo.ListSecrets(ctx, limit)
}
func (s *Service) Versions(ctx context.Context, actor domain.Principal, id string) ([]Version, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	return s.repo.ListSecretVersions(ctx, id)
}
func (s *Service) Delete(ctx context.Context, actor domain.Principal, id string) error {
	if err := authorize(actor); err != nil {
		return err
	}
	return s.repo.RetireSecret(ctx, id, actor.UserID)
}

// RewrapKey re-encrypts stored DEKs under the provider's current KEK in a
// bounded, repeatable batch. Secret ciphertext and version identity are left
// unchanged. Keep the old KEK configured until Remaining reaches zero.
func (s *Service) RewrapKey(ctx context.Context, actor domain.Principal, oldKeyID string, batchSize int) (RewrapResult, error) {
	if err := authorize(actor); err != nil {
		return RewrapResult{}, err
	}
	if ctx == nil || strings.TrimSpace(oldKeyID) == "" || len(oldKeyID) > 128 || batchSize < 1 || batchSize > 256 {
		return RewrapResult{}, ErrInvalid
	}
	newKeyID, newKEK, err := s.keys.Current(ctx)
	if err != nil {
		return RewrapResult{}, ErrKeyUnavailable
	}
	newKEK = append([]byte(nil), newKEK...)
	defer wipe(newKEK)
	if len(newKEK) != 32 || newKeyID == "" {
		return RewrapResult{}, ErrKeyUnavailable
	}
	if oldKeyID == newKeyID {
		return RewrapResult{}, ErrInvalid
	}
	oldKEK, err := s.keys.ByID(ctx, oldKeyID)
	if err != nil {
		return RewrapResult{}, ErrKeyUnavailable
	}
	oldKEK = append([]byte(nil), oldKEK...)
	defer wipe(oldKEK)
	if len(oldKEK) != 32 {
		return RewrapResult{}, ErrKeyUnavailable
	}

	result := RewrapResult{KeyID: newKeyID}
	candidates, err := s.repo.ListRewrapCandidates(ctx, oldKeyID, batchSize)
	if err != nil {
		return result, err
	}
	for _, candidate := range candidates {
		oldAAD := aadFor(candidate.Ref.ID, candidate.Ref.Version, oldKeyID, candidate.Purpose)
		dek, decryptErr := crypto.DecryptBytes(candidate.Ciphertext.WrappedDEK, oldKEK, []byte("dek:"+oldAAD))
		if decryptErr != nil {
			return result, errors.New("secret key unwrap failed during key rotation")
		}
		newAAD := aadFor(candidate.Ref.ID, candidate.Ref.Version, newKeyID, candidate.Purpose)
		wrapped, encryptErr := crypto.EncryptBytes(dek, newKEK, []byte("dek:"+newAAD))
		wipe(dek)
		if encryptErr != nil {
			return result, errors.New("secret key wrap failed during key rotation")
		}
		if err = s.repo.RewrapDEK(ctx, candidate.Ref, oldKeyID, newKeyID, wrapped, actor.UserID); err != nil {
			if errors.Is(err, ErrConflict) {
				continue
			}
			return result, err
		}
		result.Rewrapped++
	}
	result.Remaining, err = s.repo.CountVersionsByKey(ctx, oldKeyID)
	return result, err
}

// Resolve is intentionally not exposed by the HTTP or CLI management API.
// Runtime adapters receive this narrow interface and a named purpose.
func (s *Service) Resolve(ctx context.Context, request ResolutionRequest) ([]byte, error) {
	if ctx == nil || request.Ref.ID == "" || request.Ref.Version == 0 || !validPurpose(request.Purpose) ||
		request.WorkspaceID <= 0 || request.AccessProfileID == "" || request.AccessProfileRevision == 0 ||
		(request.WorkloadID != WorkloadControlManager && request.WorkloadID != WorkloadOperationWorker) ||
		(request.WorkloadID == WorkloadControlManager && (request.HostID == "" || request.OperationID != "")) ||
		(request.WorkloadID == WorkloadOperationWorker && (request.HostID == "" || request.OperationID == "")) {
		return nil, ErrInvalid
	}
	if err := s.authorizer.AuthorizeSecretResolution(ctx, request); err != nil {
		return nil, ErrUnauthorized
	}
	stored, err := s.repo.GetSecretVersion(ctx, request.Ref)
	if err != nil {
		return nil, err
	}
	meta, err := s.repo.GetSecret(ctx, request.Ref.ID)
	if err != nil {
		return nil, err
	}
	if meta.Purpose != request.Purpose {
		return nil, ErrUnauthorized
	}
	if stored.Ciphertext.AADVersion != 1 && stored.Ciphertext.AADVersion != 2 {
		return nil, errors.New("unsupported secret ciphertext AAD version")
	}
	kek, err := s.keys.ByID(ctx, stored.Ciphertext.KeyID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrKeyUnavailable, stored.Ciphertext.KeyID)
	}
	kek = append([]byte(nil), kek...)
	defer wipe(kek)
	if len(kek) != 32 {
		return nil, ErrKeyUnavailable
	}
	wrapAAD := aadFor(request.Ref.ID, request.Ref.Version, stored.Ciphertext.KeyID, request.Purpose)
	dek, err := crypto.DecryptBytes(stored.Ciphertext.WrappedDEK, kek, []byte("dek:"+wrapAAD))
	if err != nil {
		return nil, errors.New("secret key unwrap failed")
	}
	valueAAD := valueAADFor(request.Ref.ID, request.Ref.Version, stored.Ciphertext.AADVersion, stored.Ciphertext.DataAADKeyID, request.Purpose)
	plain, err := crypto.DecryptBytes(stored.Ciphertext.Value, dek, []byte("value:"+valueAAD))
	wipe(dek)
	if err != nil {
		return nil, errors.New("secret decryption failed")
	}
	return plain, nil
}
func (s *Service) encrypt(ctx context.Context, id string, version uint64, actor int, value string, purpose Purpose, now time.Time) (StoredVersion, error) {
	keyID, kek, err := s.keys.Current(ctx)
	if err != nil {
		return StoredVersion{}, fmt.Errorf("%w", ErrKeyUnavailable)
	}
	kek = append([]byte(nil), kek...)
	if len(kek) != 32 || keyID == "" {
		wipe(kek)
		return StoredVersion{}, ErrKeyUnavailable
	}
	defer wipe(kek)
	dek := make([]byte, 32)
	if _, err = io.ReadFull(s.random, dek); err != nil {
		return StoredVersion{}, err
	}
	defer func() {
		wipe(dek)
	}()
	wrapAAD := aadFor(id, version, keyID, purpose)
	wrapped, err := crypto.EncryptBytes(dek, kek, []byte("dek:"+wrapAAD))
	if err != nil {
		return StoredVersion{}, err
	}
	const aadVersion = 2
	valueAAD := valueAADFor(id, version, aadVersion, "", purpose)
	sealed, err := crypto.EncryptBytes([]byte(value), dek, []byte("value:"+valueAAD))
	if err != nil {
		return StoredVersion{}, err
	}
	return StoredVersion{Version: Version{Ref: Ref{ID: id, Version: version}, CreatedAt: now, CreatedBy: actor}, Ciphertext: Ciphertext{KeyID: keyID, AADVersion: aadVersion, WrappedDEK: wrapped, Value: sealed}}, nil
}

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func authorize(p domain.Principal) error {
	if p.UserID <= 0 || p.Role != domain.RoleOwner || p.APITokenID != 0 {
		return ErrUnauthorized
	}
	return nil
}
func validateInput(in Input) error {
	if len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 128 || len(in.Description) > 512 || len(in.Value) == 0 || len(in.Value) > 1<<20 {
		return ErrInvalid
	}
	if !validPurpose(in.Purpose) {
		return ErrInvalid
	}
	return nil
}
func validPurpose(p Purpose) bool {
	return p == PurposeSSHCredential || p == PurposeOperation || p == PurposeConfiguration
}
func aadFor(id string, v uint64, keyID string, purpose Purpose) string {
	return fmt.Sprintf("machinarchy/secret/v1/%s/%d/%s/%s", id, v, keyID, purpose)
}

func valueAADFor(id string, version uint64, aadVersion int, dataAADKeyID string, purpose Purpose) string {
	if aadVersion != 2 {
		return aadFor(id, version, dataAADKeyID, purpose)
	}
	return fmt.Sprintf("machinarchy/secret/value/v2/%s/%d/%s", id, version, purpose)
}

// StaticKeyProvider loads the encryption root key from an external bootstrap
// source. It never persists key material and can optionally retain old KEKs.
type StaticKeyProvider struct {
	KeyID    string
	Key      []byte
	Previous map[string][]byte
}

func (p *StaticKeyProvider) Current(context.Context) (string, []byte, error) {
	if p == nil || p.KeyID == "" || len(p.Key) != 32 {
		return "", nil, ErrKeyUnavailable
	}
	return p.KeyID, append([]byte(nil), p.Key...), nil
}
func (p *StaticKeyProvider) ByID(_ context.Context, id string) ([]byte, error) {
	if p == nil {
		return nil, ErrKeyUnavailable
	}
	if id == p.KeyID && len(p.Key) == 32 {
		return append([]byte(nil), p.Key...), nil
	}
	if k := p.Previous[id]; len(k) == 32 {
		return append([]byte(nil), k...), nil
	}
	return nil, ErrKeyUnavailable
}
