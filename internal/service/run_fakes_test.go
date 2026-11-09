package service

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/sshclient"
)

// memRunStore is an in-memory domain.RunStore that mimics the real stores'
// rules (final host results are never overwritten, finishing is idempotent).
type memRunStore struct {
	mu     sync.Mutex
	nextID int64
	runs   map[int64]*domain.Run
	cancel map[int64]bool
	// actors records the audit actor of every state-changing call, by method.
	actors map[string][]domain.AuditActor
	// inventory calls are not made here; this fake only stores runs.
}

func newMemRunStore() *memRunStore {
	return &memRunStore{runs: map[int64]*domain.Run{}, cancel: map[int64]bool{}, actors: map[string][]domain.AuditActor{}}
}

func (m *memRunStore) note(ctx context.Context, method string) {
	actor, _ := domain.AuditActorFrom(ctx)
	m.actors[method] = append(m.actors[method], actor)
}

func clone(run *domain.Run) *domain.Run {
	c := *run
	c.Hosts = append([]domain.RunHost(nil), run.Hosts...)
	c.Argv = append([]string(nil), run.Argv...)
	return &c
}

func recount(run *domain.Run) {
	run.Counts = domain.RunCounts{}
	for _, h := range run.Hosts {
		switch h.Status {
		case domain.RunHostPending:
			run.Counts.Pending++
		case domain.RunHostRunning:
			run.Counts.Running++
		case domain.RunHostSucceeded:
			run.Counts.Succeeded++
		case domain.RunHostFailed:
			run.Counts.Failed++
		case domain.RunHostTimedOut:
			run.Counts.TimedOut++
		case domain.RunHostCancelled:
			run.Counts.Cancelled++
		case domain.RunHostInterrupted:
			run.Counts.Interrupted++
		}
	}
}

func (m *memRunStore) CreateRun(ctx context.Context, run *domain.Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.note(ctx, "CreateRun")
	m.nextID++
	run.ID, run.Status, run.CreatedAt, run.HeartbeatAt = m.nextID, domain.RunQueued, time.Now().UTC(), time.Now().UTC()
	stored := clone(run)
	recount(stored)
	m.runs[run.ID] = stored
	return nil
}

func (m *memRunStore) GetRun(_ context.Context, workspaceID int, id int64) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[id]
	if !ok || run.WorkspaceID != workspaceID {
		return nil, domain.ErrRunNotFound
	}
	c := clone(run)
	c.CancelRequested = m.cancel[id]
	recount(c)
	return c, nil
}

func (m *memRunStore) ListRuns(_ context.Context, workspaceID int, filter domain.RunFilter) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, run := range m.runs {
		if run.WorkspaceID == workspaceID && (filter.Status == "" || string(run.Status) == filter.Status) &&
			(filter.BeforeID == 0 || run.ID < filter.BeforeID) {
			c := clone(run)
			recount(c)
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > filter.Limit && filter.Limit > 0 {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (m *memRunStore) StartRun(_ context.Context, id int64, _ string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.Status != domain.RunQueued {
		return domain.ErrRunFinished
	}
	run.Status, run.StartedAt, run.HeartbeatAt = domain.RunRunning, &now, now
	return nil
}

func (m *memRunStore) Heartbeat(_ context.Context, id int64, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.Status.Final() {
		return false, domain.ErrRunFinished
	}
	run.HeartbeatAt = now
	return m.cancel[id], nil
}

func (m *memRunStore) UpdateRunHost(_ context.Context, runID int64, hostID string, u domain.RunHostUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[runID]
	if run == nil {
		return nil
	}
	for i := range run.Hosts {
		h := &run.Hosts[i]
		if h.HostID != hostID || h.Status.Final() {
			continue
		}
		h.Status, h.ExitCode, h.Output, h.Error = u.Status, u.ExitCode, u.Output, u.Error
		if u.StartedAt != nil {
			h.StartedAt = u.StartedAt
		}
		if u.FinishedAt != nil {
			h.FinishedAt = u.FinishedAt
		}
	}
	return nil
}

func (m *memRunStore) RequestCancel(ctx context.Context, workspaceID int, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.WorkspaceID != workspaceID {
		return domain.ErrRunNotFound
	}
	if run.Status.Final() {
		return domain.ErrRunFinished
	}
	m.note(ctx, "RequestCancel")
	m.cancel[id] = true
	return nil
}

func (m *memRunStore) FinishRun(ctx context.Context, id int64, status domain.RunStatus, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
		return domain.ErrRunNotFound
	}
	if run.Status.Final() {
		return nil
	}
	m.note(ctx, "FinishRun")
	run.Status, run.FinishedAt = status, &now
	return nil
}

func (m *memRunStore) InterruptStaleRuns(_ context.Context, cutoff, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, run := range m.runs {
		if run.Status.Final() || !run.HeartbeatAt.Before(cutoff) {
			continue
		}
		for i := range run.Hosts {
			if !run.Hosts[i].Status.Final() {
				run.Hosts[i].Status = domain.RunHostInterrupted
			}
		}
		run.Status, run.FinishedAt = domain.RunInterrupted, &now
		count++
	}
	return count, nil
}

// waitFinal blocks until the run is final and returns it.
func (m *memRunStore) waitFinal(t *testing.T, id int64) *domain.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		run := m.runs[id]
		final := run != nil && run.Status.Final()
		var c *domain.Run
		if final {
			c = clone(run)
			recount(c)
		}
		m.mu.Unlock()
		if final {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %d did not finish", id)
	return nil
}

func (m *memRunStore) snapshot(id int64) *domain.Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := clone(m.runs[id])
	recount(c)
	return c
}

// runHosts is an inventory fake: ListAllHosts for target resolution and
// GetHostByIDInWorkspace for execution.
type runHosts struct {
	domain.HostStore
	mu        sync.Mutex
	hosts     map[int]*domain.Host
	listCalls int
}

func newRunHosts(hosts ...*domain.Host) *runHosts {
	r := &runHosts{hosts: map[int]*domain.Host{}}
	for _, h := range hosts {
		id, _ := strconv.Atoi(h.ID)
		r.hosts[id] = h
	}
	return r
}

func (r *runHosts) ListAllHosts(_ context.Context, workspaceID int) ([]*domain.Host, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	var out []*domain.Host
	for _, h := range r.hosts {
		if h.WorkspaceID == workspaceID {
			out = append(out, h)
		}
	}
	return out, nil
}

func (r *runHosts) GetHostByIDInWorkspace(_ context.Context, workspaceID, id int) (*domain.Host, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.hosts[id]
	if !ok || h.WorkspaceID != workspaceID {
		return nil, domain.ErrHostNotFound
	}
	c := *h
	ssh := *h.SSH
	c.SSH = &ssh
	return &c, nil
}

func (r *runHosts) mutate(id int, change func(*domain.Host)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(r.hosts[id])
}

func (r *runHosts) remove(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hosts, id)
}

type noEnvironments struct{ domain.EnvironmentStore }

func (noEnvironments) ListEnvironments(context.Context, int) ([]*domain.Environment, error) {
	return []*domain.Environment{{ID: 1, Name: "production"}}, nil
}

func runHost(id int, name, address string, port int, labels map[string]string) *domain.Host {
	return &domain.Host{
		ID: strconv.Itoa(id), WorkspaceID: 1, Name: name, IPAddress: address, EnvironmentID: 1, Labels: labels,
		Connection: domain.MethodSSH,
		SSH: &domain.SSHAccess{
			Port: port, Username: "deploy", Status: domain.HostStatusActive,
			AuthType: domain.SSHAuthType{Method: domain.AuthTypePassword, Password: "pw", ExpectedFingerprint: "SHA256:fp"},
		},
	}
}

// fakeRunner is a controllable CommandRunner.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []runnerCall
	inFlight int
	maxSeen  int
	// behave decides each call's result; nil means success with "ok".
	behave func(ctx context.Context, call runnerCall) (string, error)
}

type runnerCall struct {
	Address string
	Port    int
	Secret  string
	Command string
}

func (f *fakeRunner) Run(ctx context.Context, params sshclient.DialParams, command string) (string, error) {
	call := runnerCall{Address: params.IP, Port: params.Port, Secret: params.SecretData, Command: command}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.inFlight++
	if f.inFlight > f.maxSeen {
		f.maxSeen = f.inFlight
	}
	behave := f.behave
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if behave == nil {
		return "ok", nil
	}
	return behave(ctx, call)
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRunner) max() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxSeen
}
