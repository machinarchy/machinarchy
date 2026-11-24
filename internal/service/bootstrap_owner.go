package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/machinarchy/machinarchy/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// BootstrapOwner creates the first owner of the default workspace. It does
// nothing when an owner already exists, so it is safe to call on every start and
// from several processes at once. The password is only hashed here and is never
// logged or returned.
//
// It reports whether this call created the account.
func BootstrapOwner(ctx context.Context, users domain.UserStore, username, email, password string) (bool, error) {
	if users == nil {
		return false, errors.New("bootstrap owner requires a user store")
	}
	username = strings.ToLower(strings.TrimSpace(username))
	email = strings.ToLower(strings.TrimSpace(email))
	if username == "" || len([]byte(password)) < 12 || len([]byte(password)) > 72 {
		return false, fmt.Errorf("%w: bootstrap username is required and password must be between 12 and 72 bytes", domain.ErrInvalidInput)
	}

	// Everything this function writes is attributed to the bootstrap process, not
	// to the account it creates.
	ctx = domain.WithAuditActor(ctx, domain.AuditActor{Username: "bootstrap", Source: "bootstrap"})

	owners, err := users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID)
	if err != nil {
		return false, err
	}
	if owners > 0 {
		return false, nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, fmt.Errorf("hash bootstrap password: %w", err)
	}
	// Created as owner in one step when the store can, so a replica starting
	// at the same time never sees the account before it is the owner.
	create, atomicOwner := users.CreateUser, false
	if owners, ok := users.(ownerCreator); ok {
		create, atomicOwner = owners.CreateOwner, true
	}
	user, err := create(ctx, username, email, string(hash))
	if err != nil {
		if errors.Is(err, domain.ErrUserAlreadyExists) {
			// Another process may have won the race; only that case is benign.
			if owners, countErr := users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID); countErr == nil && owners > 0 {
				return false, nil
			}
			return false, fmt.Errorf("bootstrap username %q already exists but is not a workspace owner; choose another username or promote it manually", username)
		}
		return false, fmt.Errorf("create bootstrap owner: %w", err)
	}
	if atomicOwner {
		return true, nil
	}
	if err := users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, user.ID, domain.RoleOwner); err != nil {
		return false, fmt.Errorf("promote bootstrap owner: %w", err)
	}
	return true, nil
}

// ownerCreator creates an account directly as the workspace owner.
type ownerCreator interface {
	CreateOwner(ctx context.Context, username, email, passwordHash string) (*domain.User, error)
}
