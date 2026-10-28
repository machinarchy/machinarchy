package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
	"github.com/machinarchy/machinarchy/pkg/sshclient"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type metricsService struct {
	metricStore domain.MetricsStore
	hostStore   domain.HostStore
	sshLimit    *SSHCapacity
	policy      netpolicy.Policy
}

func NewMetricsService(ms domain.MetricsStore, hs domain.HostStore) domain.MetricsService {
	return &metricsService{
		metricStore: ms,
		hostStore:   hs,
		sshLimit:    newDefaultSSHCapacity(),
		policy:      netpolicy.Default(),
	}
}

// NewMetricsServiceWithCapacity constructs a metrics service sharing the process SSH bulkhead.
func NewMetricsServiceWithCapacity(ms domain.MetricsStore, hs domain.HostStore, capacity *SSHCapacity, opts ...ServiceOption) (domain.MetricsService, error) {
	if ms == nil || hs == nil || capacity == nil {
		return nil, errors.New("metrics service dependencies and SSH capacity are required")
	}
	options := applyOptions(opts)
	return &metricsService{metricStore: ms, hostStore: hs, sshLimit: capacity, policy: options.targetPolicy}, nil
}

func (s *metricsService) GetHistory(ctx context.Context, workspaceID int, timeRange string) ([]domain.DataPoint, error) {
	if ctx == nil {
		return nil, errors.New("metrics history context is required")
	}
	if workspaceID <= 0 {
		return nil, fmt.Errorf("%w: workspace ID must be positive", domain.ErrInvalidInput)
	}
	if s.metricStore == nil {
		return nil, errors.New("metrics store is not configured")
	}
	var limit int

	// Convert textual range limits to actual granular rows (1 point = 1 minute)
	switch timeRange {
	case "", "1h":
		limit = 60
	case "5h":
		limit = 300
	case "24h":
		limit = 1440
	default:
		return nil, fmt.Errorf("%w: unsupported metrics range %q", domain.ErrInvalidInput, timeRange)
	}

	return s.metricStore.GetStoredHistory(ctx, workspaceID, limit)
}

func (s *metricsService) GetLiveDiagnostics(ctx context.Context, workspaceID, hostID int) (result *domain.HostRuntimeDiagnostics, retErr error) {
	if ctx == nil {
		return nil, errors.New("diagnostics context is required")
	}
	if s.hostStore == nil || s.sshLimit == nil {
		return nil, errors.New("metrics service dependencies are not configured")
	}
	ctx, span := otel.Tracer("machinarchy/internal/service").Start(ctx, "host.diagnostics",
		trace.WithAttributes(attribute.Int("host.id", hostID)),
	)
	defer func() {
		if retErr != nil {
			span.SetStatus(codes.Error, "host diagnostics failed")
			span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", retErr)))
		}
		span.End()
	}()
	// 1. Fetch host entity configuration records from DB
	host, err := s.hostStore.GetHostByIDInWorkspace(ctx, workspaceID, hostID)
	if err != nil {
		return nil, fmt.Errorf("host record inaccessible: %w", err)
	}
	if host == nil || host.SSH == nil {
		return nil, domain.ErrHostNotFound
	}

	var secretData string
	if host.SSH.AuthType.Method == domain.AuthTypePrivateKey {
		secretData = host.SSH.AuthType.PrivateKey
	} else if host.SSH.AuthType.Method == domain.AuthTypePassword {
		secretData = host.SSH.AuthType.Password
	} else {
		return nil, fmt.Errorf("%w: host has no supported SSH authentication method", domain.ErrInvalidInput)
	}

	dialParams := sshclient.DialParams{
		IP:                  host.IPAddress,
		Port:                host.SSH.Port,
		Username:            host.SSH.Username,
		AuthMethod:          host.SSH.AuthType.Method,
		SecretData:          secretData,
		Passphrase:          host.SSH.AuthType.Passphrase,
		ExpectedFingerprint: host.SSH.AuthType.ExpectedFingerprint,
		Dialer:              s.policy.DialContext,
	}

	// 2. Execute Linux baseline collection telemetry matrix command block
	// We chain standard coreutils instructions together to get a clean raw metrics block
	cmd := `uptime && echo "---" && free -m && echo "---" && df -kP / | tail -n 1 && echo "---" && top -bn1 | grep "Cpu(s)"`
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	release, err := s.sshLimit.Acquire(commandCtx)
	if err != nil {
		return nil, err
	}
	defer release()
	rawOutput, err := sshclient.ExecuteRemoteCommand(commandCtx, dialParams, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to collect diagnostics over SSH connection loop: %w", err)
	}

	// 3. Parse raw string response text blocks into structured entity frames
	return parseRawLinuxDiagnostics(hostID, rawOutput)
}

func parseRawLinuxDiagnostics(hostID int, input string) (*domain.HostRuntimeDiagnostics, error) {
	diag := &domain.HostRuntimeDiagnostics{
		HostID:    hostID,
		Timestamp: time.Now().UTC(),
		Uptime:    "unknown",
	}

	sections := strings.Split(input, "---")
	if len(sections) < 4 {
		return nil, fmt.Errorf("incomplete diagnostics output: expected four sections, got %d", len(sections))
	}

	// Parse Section 0: Uptime
	diag.Uptime = strings.TrimSpace(sections[0])

	// Parse Section 1: Memory (free -m format)
	memLines := strings.Split(sections[1], "\n")
	for _, line := range memLines {
		if strings.HasPrefix(strings.TrimSpace(line), "Mem:") {
			fields := strings.Fields(line)
			if len(fields) < 4 {
				return nil, fmt.Errorf("malformed memory diagnostics line %q", line)
			}
			var err error
			if diag.MemoryTotal, err = strconv.Atoi(fields[1]); err != nil {
				return nil, fmt.Errorf("parse memory total: %w", err)
			}
			if diag.MemoryUsed, err = strconv.Atoi(fields[2]); err != nil {
				return nil, fmt.Errorf("parse memory used: %w", err)
			}
			if diag.MemoryFree, err = strconv.Atoi(fields[3]); err != nil {
				return nil, fmt.Errorf("parse free memory: %w", err)
			}
		}
	}
	if diag.MemoryTotal == 0 {
		return nil, fmt.Errorf("memory diagnostics did not contain a Mem: row")
	}

	// Parse Section 2: Storage Disk Allocation (df -h / output line)
	diskFields := strings.Fields(strings.TrimSpace(sections[2]))
	if len(diskFields) >= 5 {
		totalKB, err := strconv.ParseFloat(diskFields[1], 64)
		if err != nil {
			return nil, fmt.Errorf("parse disk total: %w", err)
		}
		usedKB, err := strconv.ParseFloat(diskFields[2], 64)
		if err != nil {
			return nil, fmt.Errorf("parse disk used: %w", err)
		}
		pctStr := strings.TrimRight(diskFields[4], "%")
		diag.DiskTotalGB = totalKB / (1024 * 1024)
		diag.DiskUsedGB = usedKB / (1024 * 1024)
		if diag.DiskUsagePct, err = strconv.ParseFloat(pctStr, 64); err != nil {
			return nil, fmt.Errorf("parse disk usage percentage: %w", err)
		}
	} else {
		return nil, fmt.Errorf("malformed disk diagnostics output %q", sections[2])
	}

	// Parse Section 3: CPU Usage percentage computation bounds
	fields := strings.Fields(sections[3])
	for i, field := range fields {
		if field == "id" || field == "id," {
			if i == 0 {
				return nil, fmt.Errorf("malformed CPU idle percentage output %q", sections[3])
			}
			field = fields[i-1]
		} else if strings.HasSuffix(field, "id,") || strings.HasSuffix(field, "id") {
			field = strings.TrimSuffix(strings.TrimSuffix(field, ","), "id")
		}
		if field != fields[i] || strings.HasSuffix(fields[i], "id,") || strings.HasSuffix(fields[i], "id") {
			idle := strings.TrimRight(strings.ReplaceAll(field, ",", "."), "%")
			idlePct, err := strconv.ParseFloat(idle, 64)
			if err != nil {
				return nil, fmt.Errorf("parse CPU idle percentage: %w", err)
			}
			diag.CPUUsagePct = 100 - idlePct
			return diag, nil
		}
	}
	return nil, fmt.Errorf("CPU diagnostics did not contain an idle percentage")
}
