// internal/service/host_service.go
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/sshclient"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// HostService contains the application use cases for managed hosts.
type HostService interface {
	RegisterHost(ctx context.Context, host *domain.Host) error
}

type hostService struct {
	repo      domain.HostStore
	publisher EventPublisher
	sshTester SSHConnectionTester
	sshLimit  *SSHCapacity
}

type EventPublisher interface {
	Publish(ctx context.Context, subject string, payload interface{}) error
}

type SSHConnectionTester interface {
	TestConnection(ctx context.Context, params sshclient.DialParams) error
}

type defaultSSHConnectionTester struct{}

func (defaultSSHConnectionTester) TestConnection(ctx context.Context, params sshclient.DialParams) error {
	return sshclient.TestConnection(ctx, params)
}

func NewHostService(r domain.HostStore, publisher EventPublisher) HostService {
	return NewHostServiceWithTester(r, publisher, defaultSSHConnectionTester{})
}

func NewHostServiceWithTester(r domain.HostStore, publisher EventPublisher, tester SSHConnectionTester) HostService {
	return &hostService{
		repo: r, publisher: publisher, sshTester: tester,
		sshLimit: newDefaultSSHCapacity(),
	}
}

// NewHostServiceWithCapacity constructs a host service using a shared SSH bulkhead.
func NewHostServiceWithCapacity(r domain.HostStore, publisher EventPublisher, capacity *SSHCapacity) (HostService, error) {
	return NewHostServiceWithTesterAndCapacity(r, publisher, defaultSSHConnectionTester{}, capacity)
}

// NewHostServiceWithTesterAndCapacity injects SSH verification and capacity ports.
func NewHostServiceWithTesterAndCapacity(r domain.HostStore, publisher EventPublisher, tester SSHConnectionTester, capacity *SSHCapacity) (HostService, error) {
	if r == nil || tester == nil || capacity == nil {
		return nil, errors.New("host service dependencies and SSH capacity are required")
	}
	return &hostService{repo: r, publisher: publisher, sshTester: tester, sshLimit: capacity}, nil
}

func (s *hostService) RegisterHost(ctx context.Context, h *domain.Host) (retErr error) {
	if ctx == nil {
		return errors.New("host registration context is required")
	}
	ctx, span := otel.Tracer("machinarchy/internal/service").Start(ctx, "host.register",
		trace.WithAttributes(attribute.String("operation", "host.register")),
	)
	defer func() {
		if retErr != nil {
			span.SetStatus(codes.Error, "host registration failed")
			span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", retErr)))
		}
		span.End()
	}()
	if s.repo == nil || s.sshTester == nil {
		return errors.New("host service dependencies are not configured")
	}
	if h == nil {
		return fmt.Errorf("%w: host is required", domain.ErrInvalidInput)
	}
	// Enforce centralized domain constraints checks instantly
	if err := h.SSHValidate(); err != nil {
		return err
	}

	// Choose signature parameters values matching deduced types matrix safely
	var secretData string
	if h.SSH.AuthType.Method == domain.AuthTypePrivateKey {
		secretData = h.SSH.AuthType.PrivateKey
	} else {
		secretData = h.SSH.AuthType.Password
	}

	dialParams := sshclient.DialParams{
		IP:                  h.IPAddress,
		Port:                h.SSH.Port,
		Username:            h.SSH.Username,
		AuthMethod:          h.SSH.AuthType.Method,
		SecretData:          secretData,
		Passphrase:          h.SSH.AuthType.Passphrase,
		ExpectedFingerprint: h.SSH.AuthType.ExpectedFingerprint,
	}
	// 3. Execute Bounded Cryptographic Network Test Verification Handshake
	testCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	release, err := s.sshLimit.Acquire(testCtx)
	if err != nil {
		return err
	}
	defer release()

	err = s.sshTester.TestConnection(testCtx, dialParams)
	if err != nil {
		var unknownKeyErr *domain.ErrHostKeyUnknown
		if errors.As(err, &unknownKeyErr) {
			return unknownKeyErr // Bubble fingerprint up intact for the handler to intercept
		}
		return err
	}

	// 4. Verification Succeeded Safely -> Persist entry directly to DB via Repo
	h.SSH.Status = domain.HostStatusActive
	h.CreatedAt = time.Now().UTC()

	if err := s.repo.CreateHost(ctx, h); err != nil {
		return fmt.Errorf("persist host: %w", err)
	}
	span.SetAttributes(attribute.String("host.id", h.ID))
	if s.publisher != nil {
		hostID, parseErr := strconv.Atoi(h.ID)
		if parseErr != nil {
			slog.Error("created host ID is not numeric; skipped host-created event", "host_id", h.ID, "error", parseErr)
			return nil
		}
		event := domain.HostCreatedEvent{
			HostID: hostID, Hostname: h.Hostname, EnvironmentID: h.EnvironmentID,
			Timestamp: h.CreatedAt,
		}
		if err := s.publisher.Publish(ctx, "machinarchy.host.created", event); err != nil {
			// The database write already committed. Report the event delivery failure
			// operationally without making the client retry an already-created host.
			slog.Error("failed to publish host-created event", "host_id", h.ID, "error", err)
		}
	}
	return nil
}
