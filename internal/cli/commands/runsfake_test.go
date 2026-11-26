package commands_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
	"github.com/machinarchy/machinarchy/internal/cli/config"
)

// runFake is a managed API with runs. A run created by SubmitOperation is
// driven by a script: the n-th poll returns the n-th state (the last one
// repeats), so a test controls time by counting polls.
type runFake struct {
	*fakeClient
	mu sync.Mutex

	// store holds finished or hand-made runs by ID; order lists them newest first.
	store map[string]*api.Operation
	order []string

	// scripts drive live runs; polls counts the GetOperation calls per run.
	scripts map[string][]api.Operation
	polls   map[string]int

	// script builds the script of a new run from its hosts.
	script func(hosts []api.Host) []api.Operation
	// pollErr fails the n-th poll (1-based) of any run.
	pollErr map[int]error
	// onPoll runs at the start of the n-th poll (1-based) of any run.
	onPoll func(n int)

	submits   []api.OperationInput
	submitErr error
	onSubmit  func()
	changed   bool
	created   []string

	cancels      []string
	cancelScript func(op api.Operation) []api.Operation
	outputByHost map[string]string // "runID/hostID/stream"
}

func newRunFake(hosts ...api.Host) *runFake {
	client := &fakeClient{hosts: hosts, envs: []api.Environment{{ID: 1, Name: "production"}, {ID: 2, Name: "staging"}}}

	return &runFake{fakeClient: client, store: map[string]*api.Operation{}, scripts: map[string][]api.Operation{},
		polls: map[string]int{}, pollErr: map[int]error{}, outputByHost: map[string]string{}}
}

// fleet makes n hosts named prefix-01.. in env, with the tag tier=prefix.
func fleet(prefix string, n, env int) []api.Host {
	hosts := make([]api.Host, n)
	for i := range hosts {
		name := fmt.Sprintf("%s-%02d", prefix, i+1)
		hosts[i] = managedHost("h-"+name, name, env, "10.0.0.1:22", map[string]string{"tier": prefix})
	}

	return hosts
}

func exit(code int) *int { return &code }

// target is one host of a run in a state, with what it printed.
func target(name, state string, code *int, stdout, class string) api.OperationTarget {
	t := api.OperationTarget{Spec: api.OperationTargetSpec{HostID: "h-" + name, DisplayName: name}, State: state}
	if state != "Pending" && state != "Running" {
		t.Result = &api.TargetResult{ExitCode: code, Stdout: stdout, ErrorClass: class}
	}

	return t
}

func operation(id, state string, targets ...api.OperationTarget) api.Operation {
	return api.Operation{ID: id, Spec: api.OperationSpec{Run: &api.RunSpec{Argv: []string{"systemctl", "is-active", "nginx"}, TimeoutSeconds: 60, Concurrency: 5}},
		Status: api.OperationStatus{State: state, Targets: targets}}
}

// runID builds a run ID whose short form is r-XXXXXXXX (8 hex digits).
func runID(short string, n int) string {
	return fmt.Sprintf("%s-0000-4000-8000-%012d", short, n)
}

// put stores a finished run, newest first.
func (r *runFake) put(op api.Operation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copyOf := op
	r.store[op.ID] = &copyOf
	r.order = append([]string{op.ID}, r.order...)
}

func (r *runFake) PreviewTargets(_ context.Context, rule api.TargetRule) (*api.TargetSet, error) {
	set := &api.TargetSet{}
	named := map[string]bool{}
	byName := func(name string) (api.Host, bool) {
		for _, host := range r.hosts {
			if strings.EqualFold(host.Metadata.Name, name) || host.Metadata.ID == name {
				return host, true
			}
		}

		return api.Host{}, false
	}
	add := func(host api.Host) {
		if !named[host.Metadata.ID] {
			named[host.Metadata.ID] = true
			set.Hosts = append(set.Hosts, host)
		}
	}
	switch {
	case rule.All:
		for _, host := range r.hosts {
			add(host)
		}
	case rule.Selector != "":
		for _, host := range r.hosts {
			if r.matches(host, rule.Selector) {
				add(host)
			}
		}
	}
	for _, name := range rule.Include {
		if host, ok := byName(name); ok {
			add(host)
		} else {
			set.UnknownIncludes = append(set.UnknownIncludes, name)
		}
	}
	for _, name := range rule.Exclude {
		host, ok := byName(name)
		if !ok {
			set.UnknownExcludes = append(set.UnknownExcludes, name)

			continue
		}
		kept := set.Hosts[:0:0]
		for _, h := range set.Hosts {
			if h.Metadata.ID != host.Metadata.ID {
				kept = append(kept, h)
			}
		}
		set.Hosts = kept
	}
	ids := make([]string, len(set.Hosts))
	for i, host := range set.Hosts {
		ids[i] = host.Metadata.ID
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	set.TargetsHash = hex.EncodeToString(sum[:8])

	return set, nil
}

// matches evaluates comma-separated key=value terms; environment is a name.
func (r *runFake) matches(host api.Host, selector string) bool {
	for _, term := range strings.Split(selector, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(term), "=")
		if !ok {
			return false
		}
		if key == "environment" {
			found := false
			for _, env := range r.envs {
				found = found || (env.Name == value && env.ID == host.Metadata.EnvironmentID)
			}
			if !found {
				return false
			}

			continue
		}
		if host.Metadata.Labels[key] != value {
			return false
		}
	}

	return true
}

func (r *runFake) SubmitOperation(ctx context.Context, input api.OperationInput) (*api.Operation, error) {
	r.mu.Lock()
	r.submits = append(r.submits, input)
	hook := r.onSubmit
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.submitErr != nil {
		return nil, r.submitErr
	}
	if r.changed {
		return nil, &api.Error{Status: 409, Message: "the hosts changed", Fields: map[string]string{"code": "targets_changed"}}
	}

	set, _ := r.PreviewTargets(ctx, input.Targets)
	r.mu.Lock()
	defer r.mu.Unlock()
	id := runID("9a8b7c6d", len(r.submits))
	r.created = append(r.created, id)
	var script []api.Operation
	if r.script != nil {
		script = r.script(set.Hosts)
	} else {
		var running, done []api.OperationTarget
		for _, host := range set.Hosts {
			running = append(running, api.OperationTarget{Spec: api.OperationTargetSpec{HostID: host.Metadata.ID, DisplayName: host.Metadata.Name}, State: "Running"})
			done = append(done, target(host.Metadata.Name, "Succeeded", exit(0), "active\n", ""))
		}
		script = []api.Operation{operation("", "Running", running...), operation("", "Succeeded", done...)}
	}
	for i := range script {
		script[i].ID = id
		script[i].Spec = api.OperationSpec{Run: input.Run, Capability: input.Capability}
	}
	r.scripts[id] = script
	first := script[0]
	first.Status.State = "Queued"

	return &first, nil
}

func (r *runFake) GetOperation(ctx context.Context, id string) (*api.Operation, error) {
	r.mu.Lock()
	r.polls[id]++
	n := r.polls[id]
	hook := r.onPoll
	err := r.pollErr[n]
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if script := r.scripts[id]; len(script) > 0 {
		op := script[min(n-1, len(script)-1)]

		return &op, nil
	}
	if op, ok := r.store[id]; ok {
		copyOf := *op

		return &copyOf, nil
	}

	return nil, &api.Error{Status: 404, Message: "resource not found"}
}

func (r *runFake) CancelOperation(_ context.Context, id string) (*api.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, id)
	var current api.Operation
	if script := r.scripts[id]; len(script) > 0 {
		current = script[min(max(r.polls[id]-1, 0), len(script)-1)]
	} else if op, ok := r.store[id]; ok {
		current = *op
	}
	if r.cancelScript != nil {
		r.scripts[id] = r.cancelScript(current)
		r.polls[id] = 0
	}
	asked := current
	asked.Status.State = "CancelRequested"

	return &asked, nil
}

func (r *runFake) ListOperations(_ context.Context, query api.OperationQuery) (api.Page[api.OperationSummary], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var items []api.OperationSummary
	for _, id := range r.order {
		op := r.store[id]
		counts := map[string]int{}
		for _, t := range op.Status.Targets {
			counts[t.State]++
		}
		items = append(items, api.OperationSummary{ID: id, State: op.Status.State, CreatedAt: time.Now().Add(-2 * time.Minute), Targets: counts})
	}
	if query.Limit > 0 && len(items) > query.Limit {
		items = items[:query.Limit]
	}

	return api.Page[api.OperationSummary]{Items: items}, nil
}

func (r *runFake) TargetOutput(_ context.Context, runID, hostID, stream string, from int64) (*api.TargetOutput, error) {
	text, ok := r.outputByHost[runID+"/"+hostID+"/"+stream]
	if !ok {
		return &api.TargetOutput{OperationID: runID, HostID: hostID, Stream: stream, State: "Succeeded"}, nil
	}
	end := min(int(from)+6, len(text)) // six bytes a poll: escape sequences are split
	r.mu.Lock()
	live := end < len(text) && r.polls[runID] < 1000
	r.mu.Unlock()

	return &api.TargetOutput{OperationID: runID, HostID: hostID, Stream: stream, Text: text[from:end], From: from,
		Next: int64(end), Bytes: int64(len(text)), Live: live, State: "Running"}, nil
}

func (r *runFake) submitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.submits)
}

func (r *runFake) pollCount(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.polls[id]
}

// runCfg says how a test runs the CLI.
type runCfg struct {
	terminal bool
	stdin    string
	ctx      context.Context
	// protected guards these envs on this computer.
	protected []string
}

// runCLI runs the CLI against a runFake. Polling is fast.
func runCLI(t *testing.T, fake *runFake, c runCfg, args ...string) result {
	t.Helper()
	isolate(t)
	t.Cleanup(commands.SetRunPoll(time.Millisecond))
	cfg := writeConfig(t, "http://127.0.0.1:1", "tok")
	if len(c.protected) > 0 {
		data, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		line := "    protected-envs: [" + strings.Join(c.protected, ", ") + "]\n"
		if err := os.WriteFile(cfg, append(data, []byte(line)...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var out, errOut bytes.Buffer
	code := cli.Run(ctx, cli.Options{
		Streams:       cli.IOStreams{In: strings.NewReader(c.stdin), Out: &out, ErrOut: &errOut},
		ClientBuilder: fake.builder(),
		Groups:        commands.DefaultGroups(),
		IsTerminal:    func(io.Reader) bool { return c.terminal },
	}, append([]string{"--config", cfg}, args...))

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func (r *runFake) builder() cli.APIClientBuilder {
	return func(config.Target) (api.Client, error) { return r, nil }
}
