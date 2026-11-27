package commands_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
	"github.com/machinarchy/machinarchy/internal/cli/config"
)

type result struct {
	code   int
	stdout string
	stderr string
}

func isolate(t *testing.T) {
	t.Helper()

	t.Setenv("MACHINARCHY_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	for _, name := range []string{
		"MACHINARCHY_CONTEXT", "MACHINARCHY_ENV", "MACHINARCHY_OUTPUT",
		"MACHINARCHY_VERBOSE", "MACHINARCHY_SERVER", "MACHINARCHY_TOKEN",
		"MACHINARCHY_REQUEST_TIMEOUT", "MACHINARCHY_QUIET", "MACHINARCHY_NO_HEADERS",
		"MACHINARCHY_NO_INPUT", "MACHINARCHY_YES",
		"MACHINARCHY_ENV", "MACHINARCHY_OUTPUT", "MACHINARCHY_VERBOSE", "MACHINARCHY_SERVER", "MACHINARCHY_TOKEN",
		"MACHINARCHY_REQUEST_TIMEOUT", "MACHINARCHY_QUIET", "MACHINARCHY_NO_HEADERS", "MACHINARCHY_NO_INPUT",
		"MACHINARCHY_PLAIN", "MACHINARCHY_NO_DEPRECATION_WARNINGS",
	} {
		t.Setenv(name, "")
	}
}

func execute(t *testing.T, builder cli.APIClientBuilder, stdin string, args ...string) result {
	t.Helper()
	isolate(t)

	var out, errOut bytes.Buffer
	if args == nil {
		args = []string{}
	}
	code := cli.Run(context.Background(), cli.Options{
		Streams:       cli.IOStreams{In: strings.NewReader(stdin), Out: &out, ErrOut: &errOut},
		ClientBuilder: builder,
		Groups:        commands.DefaultGroups(),
	}, args)

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func writeConfig(t *testing.T, server, token string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("version: 2\nactive: test\nservers:\n  test:\n    url: %s\n", server)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Both credentials hold the same secret, so a command works whether it runs
	// with the saved API token or, as an owner command does, the saved session.
	credentials := fmt.Sprintf("version: 2\nservers:\n  test:\n    session:\n      kind: session\n      secret: %s\n    token:\n      kind: token\n      secret: %s\n", token, token)
	if err := os.WriteFile(config.CredentialsPath(path), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// fakeClient is an in-memory managed API. ListHosts filters by environment
// and simple KEY=VALUE selectors server-side, like the real API, and pages
// pageSize hosts at a time when pageSize is set.
type fakeClient struct {
	checkReason string
	hosts       []api.Host
	envs        []api.Environment
	profiles    []api.AccessProfile
	ops         map[string]*api.Operation
	err         error
	pageSize    int

	submitted []api.OperationInput
	outputs   map[string]string // "op/host" -> the full stream, served in 6-byte steps
	deleted   []string
	trusted   []string
	moves     []string
	createdIn []int
	oldServer bool // ignores environments on hosts, like a server before C-01
	calls     int
}

var _ api.Client = (*fakeClient)(nil)

func (f *fakeClient) ListHosts(_ context.Context, query api.HostQuery) (api.Page[api.Host], error) {
	f.calls++
	if f.err != nil {
		return api.Page[api.Host]{}, f.err
	}
	var matched []api.Host
	for _, host := range f.hosts {
		if query.EnvironmentID > 0 && host.Metadata.EnvironmentID != query.EnvironmentID {
			continue
		}
		if key, value, ok := strings.Cut(query.Selector, "="); ok && host.Metadata.Labels[key] != value {
			continue
		}
		matched = append(matched, host)
	}
	start := 0
	if query.Cursor != "" {
		fmt.Sscan(query.Cursor, &start)
	}
	if f.pageSize == 0 || start+f.pageSize >= len(matched) {
		return api.Page[api.Host]{Items: matched[start:]}, nil
	}
	return api.Page[api.Host]{Items: matched[start : start+f.pageSize], NextCursor: fmt.Sprint(start + f.pageSize)}, nil
}

func (f *fakeClient) GetHost(_ context.Context, id string) (*api.Host, error) {
	for _, host := range f.hosts {
		if host.Metadata.ID == id {
			return &host, nil
		}
	}
	return nil, &api.Error{Status: 404, Message: "resource not found"}
}

func (f *fakeClient) CreateHost(_ context.Context, input api.HostInput) (*api.Host, error) {
	host := api.Host{Metadata: api.HostMetadata{ID: fmt.Sprint(len(f.hosts) + 100), Name: input.Name, Labels: input.Labels, Revision: 1, Generation: 1}, Spec: input.Spec}
	f.createdIn = append(f.createdIn, input.EnvironmentID)
	if !f.oldServer {
		host.Metadata.EnvironmentID = input.EnvironmentID
	}
	f.hosts = append(f.hosts, host)
	return &host, nil
}

func (f *fakeClient) DeleteHost(_ context.Context, id string, revision uint64) error {
	f.deleted = append(f.deleted, fmt.Sprintf("%s@%d", id, revision))
	return nil
}

func (f *fakeClient) TrustHostKeys(_ context.Context, id string, generation uint64, fingerprints []string, replace bool) (*api.Host, error) {
	for i := range f.hosts {
		if f.hosts[i].Metadata.ID == id {
			if f.hosts[i].Metadata.Generation != generation {
				return nil, &api.Error{Status: 409, Message: "resource revision conflict"}
			}
			if len(f.hosts[i].Spec.AccessTarget.Trust) > 0 && !replace {
				return nil, &api.Error{Status: 409, Message: "the Host already trusts host keys"}
			}
			f.hosts[i].Spec.AccessTarget.Trust = fingerprints
			f.hosts[i].Metadata.Generation++
			f.trusted = append(f.trusted, id+"="+strings.Join(fingerprints, ","))
			return &f.hosts[i], nil
		}
	}
	return nil, &api.Error{Status: 404, Message: "resource not found"}
}

func (f *fakeClient) CheckHost(_ context.Context, id string) (*api.HostCheck, error) {
	for _, host := range f.hosts {
		if host.Metadata.ID == id {
			check := api.HostCheck{HostID: id, Ready: true, Reachable: true, Reason: "SSHVerified"}
			if f.checkReason != "" {
				check.Ready, check.Reason = false, f.checkReason
			}
			return &check, nil
		}
	}
	return nil, &api.Error{Status: 404, Message: "resource not found"}
}

func (f *fakeClient) MoveHost(_ context.Context, id string, revision, generation uint64, _ api.HostSpec, environmentID int) error {
	f.moves = append(f.moves, fmt.Sprintf("%s->%d", id, environmentID))
	for i := range f.hosts {
		if f.hosts[i].Metadata.ID == id {
			if f.hosts[i].Metadata.Revision != revision || f.hosts[i].Metadata.Generation != generation {
				return &api.Error{Status: 409, Message: "resource revision conflict"}
			}
			if !f.oldServer {
				f.hosts[i].Metadata.EnvironmentID = environmentID
				f.hosts[i].Metadata.Revision++
			}
			return nil
		}
	}
	return &api.Error{Status: 404, Message: "resource not found"}
}

func (f *fakeClient) SetHostLabels(_ context.Context, id string, revision uint64, labels map[string]string) (*api.Host, error) {
	for i := range f.hosts {
		if f.hosts[i].Metadata.ID == id {
			if f.hosts[i].Metadata.Revision != revision {
				return nil, &api.Error{Status: 409, Message: "resource revision conflict"}
			}
			f.hosts[i].Metadata.Labels = labels
			f.hosts[i].Metadata.Revision++
			return &f.hosts[i], nil
		}
	}
	return nil, &api.Error{Status: 404, Message: "resource not found"}
}

func (f *fakeClient) ListEnvironments(context.Context) ([]api.Environment, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.envs, nil
}

func (f *fakeClient) ListAccessProfiles(context.Context, string) (api.Page[api.AccessProfile], error) {
	return api.Page[api.AccessProfile]{Items: f.profiles}, nil
}

func (f *fakeClient) PreviewTargets(ctx context.Context, rule api.TargetRule) (*api.TargetSet, error) {
	page, err := f.ListHosts(ctx, api.HostQuery{Selector: rule.Selector})
	if err != nil {
		return nil, err
	}
	set := &api.TargetSet{TargetsHash: fmt.Sprintf("hash-%d", len(page.Items))}
	if rule.Selector != "" || rule.All {
		set.Hosts = page.Items
	}
	for _, name := range rule.Include {
		if host, err := pickName(f.hosts, name); err == nil {
			set.Hosts = append(set.Hosts, host)
		} else {
			set.UnknownIncludes = append(set.UnknownIncludes, name)
		}
	}
	return set, nil
}

func pickName(hosts []api.Host, name string) (api.Host, error) {
	for _, host := range hosts {
		if host.Metadata.Name == name || host.Metadata.ID == name {
			return host, nil
		}
	}
	return api.Host{}, fmt.Errorf("no host %q", name)
}

func (f *fakeClient) SubmitOperation(_ context.Context, input api.OperationInput) (*api.Operation, error) {
	f.submitted = append(f.submitted, input)
	op := &api.Operation{ID: fmt.Sprintf("op-%d", len(f.submitted)), Status: api.OperationStatus{State: "Queued"}}
	if f.ops == nil {
		f.ops = map[string]*api.Operation{}
	}
	f.ops[op.ID] = op
	return op, nil
}

func (f *fakeClient) GetOperation(_ context.Context, id string) (*api.Operation, error) {
	op, ok := f.ops[id]
	if !ok {
		return nil, &api.Error{Status: 404, Message: "resource not found"}
	}
	// Each poll moves the operation along: Queued -> Running -> Succeeded.
	next := map[string]string{"Queued": "Running", "Running": "Succeeded"}
	current := *op
	if state, ok := next[op.Status.State]; ok {
		op.Status.State = state
		if state == "Succeeded" {
			zero := 0
			op.Status.Targets = []api.OperationTarget{{Spec: api.OperationTargetSpec{DisplayName: "web-01"}, State: "Succeeded",
				Result: &api.TargetResult{ExitCode: &zero, Output: "up 3 days\n"}}}
		}
	}
	return &current, nil
}

// TargetOutput serves the stream 6 bytes per call, live until the end.
func (f *fakeClient) TargetOutput(_ context.Context, operationID, hostID, stream string, from int64) (*api.TargetOutput, error) {
	f.calls++
	text, ok := f.outputs[operationID+"/"+hostID+"/"+stream]
	if !ok {
		return nil, &api.Error{Status: 404, Message: "resource not found"}
	}
	end := min(int(from)+6, len(text))
	return &api.TargetOutput{OperationID: operationID, HostID: hostID, Stream: stream, Text: text[from:end],
		From: from, Next: int64(end), Bytes: int64(len(text)), Live: end < len(text), State: "Running"}, nil
}

func (f *fakeClient) CreateHostsBatch(context.Context, []api.HostInput, bool) (*api.HostBatchResult, error) {
	return nil, &api.Error{Status: 404, Message: "resource not found"}
}

func (f *fakeClient) CancelOperation(_ context.Context, id string) (*api.Operation, error) {
	return &api.Operation{ID: id, Status: api.OperationStatus{State: "CancelRequested"}}, nil
}

func (f *fakeClient) ListOperations(context.Context, api.OperationQuery) (api.Page[api.OperationSummary], error) {
	return api.Page[api.OperationSummary]{Items: []api.OperationSummary{{ID: "op-9", State: "Succeeded", Targets: map[string]int{"Succeeded": 2}}}}, nil
}

func fake(client *fakeClient) cli.APIClientBuilder {
	return func(config.Target) (api.Client, error) { return client, nil }
}

func managedHost(id, name string, environment int, endpoint string, labels map[string]string) api.Host {
	return api.Host{Metadata: api.HostMetadata{ID: id, Name: name, EnvironmentID: environment, Labels: labels, Revision: 1},
		Spec: api.HostSpec{AccessTarget: api.HostAccessTarget{Endpoint: endpoint}}, Status: api.HostStatus{Phase: "Active"}}
}

func fixtures() *fakeClient {
	return &fakeClient{
		hosts: []api.Host{
			managedHost("1", "web-01", 1, "10.0.0.1:22", map[string]string{"tier": "web"}),
			managedHost("2", "web-02", 1, "10.0.0.2:22", map[string]string{"tier": "web"}),
			managedHost("3", "db-01", 2, "10.0.0.3:22", map[string]string{"tier": "db"}),
			managedHost("4", "dup", 1, "10.0.0.4:22", nil),
			managedHost("5", "dup", 2, "10.0.0.5:22", nil),
		},
		envs:     []api.Environment{{ID: 1, Name: "prod"}, {ID: 2, Name: "dev"}},
		profiles: []api.AccessProfile{{ID: "p-1", Name: "deploy", CurrentRevision: 1}},
	}
}

func dataLines(output string) []string {
	trimmed := strings.TrimRight(output, "\n")
	if trimmed == "" {
		return nil
	}

	return strings.Split(trimmed, "\n")
}

// ---- registration and compatibility ---------------------------------------

func TestCommandTreeKeepsEveryExistingCommand(t *testing.T) {
	isolate(t)
	root := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})

	want := map[string][]string{
		"envs":           {"add", "remove", "use"},
		"get":            {"agent-tokens"},
		"run":            nil,
		"config":         {"path", "show"},
		"tokens":         {"create", "revoke"},
		"members":        {"add", "invite", "invites", "link", "remove", "revoke", "role"},
		"audit":          {"export", "prune", "verify"},
		"logout":         nil,
		"whoami":         nil,
		"switch":         nil,
		"runtime-config": {"get", "history", "publish", "rollback", "status", "validate"},
		"secret":         {"create", "delete", "list", "rewrap", "rotate", "versions"},
		"version":        nil,
		"login":          nil,
		"access":         {"add", "remove", "show"},
		"hosts":          {"add", "check", "enroll", "import", "move", "remove", "revoke-agent", "show", "tag", "trust"},
	}

	var top []string
	for _, command := range root.Commands() {
		top = append(top, command.Name())
	}
	sort.Strings(top)

	wantTop := make([]string, 0, len(want))
	for name := range want {
		wantTop = append(wantTop, name)
	}
	sort.Strings(wantTop)
	if strings.Join(top, ",") != strings.Join(wantTop, ",") {
		t.Fatalf("top-level commands = %v, want %v", top, wantTop)
	}

	for name, children := range want {
		command, _, err := root.Find([]string{name})
		if err != nil || command.Name() != name {
			t.Fatalf("command %q not found: %v", name, err)
		}

		var got []string
		for _, child := range command.Commands() {
			got = append(got, child.Name())
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(children, ",") {
			t.Errorf("%s subcommands = %v, want %v", name, got, children)
		}
	}
}

func TestEveryCommandIsWellFormed(t *testing.T) {
	isolate(t)
	root := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})

	groups := map[string]bool{}
	for _, group := range root.Groups() {
		groups[group.ID] = true
	}

	var walk func(command *cobra.Command)
	walk = func(command *cobra.Command) {
		for _, child := range command.Commands() {
			path := child.CommandPath()

			if strings.TrimSpace(child.Short) == "" {
				t.Errorf("%s has no short description", path)
			}
			if command == root && !groups[child.GroupID] {
				t.Errorf("%s is not in a registered command group (%q)", path, child.GroupID)
			}
			if child.HasSubCommands() {
				if child.Args == nil {
					t.Errorf("%s is a command group without an Args validator, so a typo would be silently accepted", path)
				}
			} else if child.RunE == nil && child.Run == nil {
				t.Errorf("%s does nothing", path)
			}

			walk(child)
		}
	}
	walk(root)
}

func TestHelpWorksForEveryCommand(t *testing.T) {
	isolate(t)
	root := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})

	var paths [][]string
	var walk func(command *cobra.Command)
	walk = func(command *cobra.Command) {
		for _, child := range command.Commands() {
			paths = append(paths, strings.Fields(child.CommandPath())[1:])
			walk(child)
		}
	}
	walk(root)

	if len(paths) < 20 {
		t.Fatalf("only %d commands found; the tree looks truncated", len(paths))
	}

	for _, path := range paths {
		got := execute(t, nil, "", append(append([]string(nil), path...), "--help")...)

		if got.code != 0 || !strings.Contains(got.stdout, "Usage:") {
			t.Errorf("%s --help: code %d, stdout %q, stderr %q", strings.Join(path, " "), got.code, got.stdout, got.stderr)
		}
	}
}

func TestHostsHelpHasExamples(t *testing.T) {
	got := execute(t, nil, "", "hosts", "--help")

	if !strings.Contains(got.stdout, "Examples:") || !strings.Contains(got.stdout, "machinarchy hosts show") {
		t.Fatalf("help lacks examples:\n%s", got.stdout)
	}
}

// ---- version ---------------------------------------------------------------

func TestVersion(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		got := execute(t, nil, "", "version")

		if got.code != 0 || !strings.HasPrefix(got.stdout, "machinarchy ") {
			t.Fatalf("result = %+v", got)
		}
		// N-10: a pipe gets no logotype and no escape codes.
		if strings.ContainsAny(got.stdout, "◆*\x1b") {
			t.Errorf("output to a pipe has a logotype or colour:\n%q", got.stdout)
		}
		for _, want := range []string{"commit:", "built:", "go:", "platform:", runtime.GOOS + "/" + runtime.GOARCH} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("output lacks %q:\n%s", want, got.stdout)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		got := execute(t, nil, "", "version", "-o", "json")

		var decoded map[string]string
		if err := json.Unmarshal([]byte(got.stdout), &decoded); err != nil || got.code != 0 {
			t.Fatalf("result = %+v, err = %v", got, err)
		}
		for _, key := range []string{"version", "commit", "buildTime", "goVersion", "platform"} {
			if decoded[key] == "" {
				t.Errorf("JSON lacks %q: %v", key, decoded)
			}
		}
	})

	t.Run("yaml", func(t *testing.T) {
		got := execute(t, nil, "", "version", "-o", "yaml")
		if got.code != 0 || !strings.Contains(got.stdout, "goVersion: ") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("works with a damaged config file", func(t *testing.T) {
		broken := filepath.Join(t.TempDir(), "broken.yaml")
		if err := os.WriteFile(broken, []byte("contexts: [unterminated"), 0o600); err != nil {
			t.Fatal(err)
		}

		got := execute(t, nil, "", "--config", broken, "version")
		if got.code != 0 {
			t.Fatalf("version failed on a damaged config: %+v", got)
		}
	})

	t.Run("rejects arguments and bad formats", func(t *testing.T) {
		if got := execute(t, nil, "", "version", "extra"); got.code != clierr.ExitUsage {
			t.Errorf("extra argument: exit code = %d", got.code)
		}
		if got := execute(t, nil, "", "version", "-o", "xml"); got.code != clierr.ExitUsage {
			t.Errorf("bad format: exit code = %d", got.code)
		}
	})
}

// ---- hosts list -------------------------------------------------------------

func TestHostsList(t *testing.T) {
	cfg := writeConfig(t, "http://127.0.0.1:1", "tok")
	run := func(t *testing.T, client *fakeClient, args ...string) result {
		t.Helper()

		return execute(t, fake(client), "", append([]string{"--config", cfg}, args...)...)
	}

	t.Run("lists hosts as a table", func(t *testing.T) {
		got := run(t, fixtures(), "hosts")

		if got.code != 0 || got.stderr != "" {
			t.Fatalf("result = %+v", got)
		}
		lines := dataLines(got.stdout)
		if len(lines) != 6 || !strings.HasPrefix(lines[0], "NAME") {
			t.Fatalf("output:\n%s", got.stdout)
		}
	})

	t.Run("json and yaml keep the API field names", func(t *testing.T) {
		got := run(t, fixtures(), "hosts", "-o", "json")
		var decoded []map[string]any
		if err := json.Unmarshal([]byte(got.stdout), &decoded); err != nil || len(decoded) != 5 {
			t.Fatalf("err = %v, hosts = %d\n%s", err, len(decoded), got.stdout)
		}
		got = run(t, fixtures(), "hosts", "-o", "yaml")
		if got.code != 0 || !strings.Contains(got.stdout, "name: web-01") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("follows every page and passes the selector", func(t *testing.T) {
		client := fixtures()
		client.pageSize = 2
		got := run(t, client, "hosts", "-q")
		if got.stdout != "web-01\nweb-02\ndb-01\ndup\ndup\n" || client.calls != 3 {
			t.Fatalf("stdout = %q after %d calls", got.stdout, client.calls)
		}
		got = run(t, fixtures(), "hosts", "-l", "tier=web", "-q")
		if got.stdout != "web-01\nweb-02\n" {
			t.Fatalf("selector stdout = %q", got.stdout)
		}
	})

	t.Run("environment filter by name, shorthand and id", func(t *testing.T) {
		for _, args := range [][]string{{"hosts", "--env", "prod"}, {"hosts", "-n", "PROD"}, {"hosts", "--env", "1"}} {
			got := run(t, fixtures(), append(args, "-q")...)
			if got.stdout != "web-01\nweb-02\ndup\n" {
				t.Errorf("%v = %q", args, got.stdout)
			}
		}
	})

	t.Run("an unknown environment is exit 6", func(t *testing.T) {
		got := run(t, fixtures(), "hosts", "--env", "staging")

		if got.code != clierr.ExitNotFound || !strings.Contains(got.stderr, `environment "staging" was not found`) {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("an empty inventory prints a message", func(t *testing.T) {
		got := run(t, &fakeClient{}, "hosts")
		if got.code != 0 || got.stdout == "" {
			t.Fatalf("result = %+v", got)
		}
		asJSON := run(t, &fakeClient{}, "hosts", "-o", "json")
		if strings.TrimSpace(asJSON.stdout) != "null" && strings.TrimSpace(asJSON.stdout) != "[]" {
			t.Errorf("empty JSON = %q", asJSON.stdout)
		}
	})

	t.Run("invalid usage is exit 2 and never reaches the API", func(t *testing.T) {
		called := false
		builder := func(config.Target) (api.Client, error) {
			called = true
			return fixtures(), nil
		}

		for _, args := range [][]string{{"hosts", "-o", "xml"}, {"hosts", "--status", "bogus"}, {"get", "hosts"}, {"create", "host"}, {"label", "host"}} {
			got := execute(t, builder, "", append([]string{"--config", cfg}, args...)...)
			if got.code != clierr.ExitUsage {
				t.Errorf("%v: exit code = %d, stderr %q", args, got.code, got.stderr)
			}
		}
		if called {
			t.Error("the API client was built for an invalid command line")
		}
	})

	t.Run("API failures map to exit codes with hints", func(t *testing.T) {
		tests := []struct {
			name     string
			err      error
			wantCode int
			wantHint string
		}{
			{"unauthorized", &api.Error{Status: 401, Message: "expired"}, clierr.ExitAuth, "machinarchy login"},
			{"forbidden", &api.Error{Status: 403, Message: "no"}, clierr.ExitAuth, "permitted"},
			{"server error", &api.Error{Status: 502, Message: "bad gateway"}, clierr.ExitUnavailable, "Retry later"},
			{"unreachable", clierr.Newf(clierr.ExitUnavailable, "check the URL", "cannot reach the API server"), clierr.ExitUnavailable, "check the URL"},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				got := run(t, &fakeClient{err: test.err}, "hosts")

				if got.code != test.wantCode {
					t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, test.wantCode, got.stderr)
				}
				if !strings.Contains(got.stderr, "Error: ") || !strings.Contains(got.stderr, test.wantHint) {
					t.Errorf("stderr = %q", got.stderr)
				}
				if got.stdout != "" {
					t.Errorf("stdout = %q, want nothing", got.stdout)
				}
			})
		}
	})

	t.Run("no server explains how to sign in", func(t *testing.T) {
		got := execute(t, fake(fixtures()), "", "--config", filepath.Join(t.TempDir(), "absent.yaml"), "hosts")

		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "machinarchy login YOUR-SERVER-URL") {
			t.Fatalf("result = %+v", got)
		}
	})
}

// ---- management commands ---------------------------------------------------

type apiRequest struct {
	Method string
	Path   string
	Auth   string
	Body   []byte
}

type apiServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []apiRequest
}

func newAPIServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *apiServer {
	t.Helper()

	server := &apiServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)

		server.mu.Lock()
		server.requests = append(server.requests, apiRequest{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body.Bytes()})
		server.mu.Unlock()

		handler(w, r)
	}))
	t.Cleanup(server.Close)

	return server
}

func (s *apiServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *apiServer) last(t *testing.T) apiRequest {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("the server received no request")
	}

	return s.requests[len(s.requests)-1]
}

func jsonReply(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestManagementCommandsFailBeforeTouchingTheServer(t *testing.T) {
	server := newAPIServer(t, jsonReply(200, `{}`))
	path := writeConfig(t, server.URL, "tok")

	tests := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"publish without a revision", "", []string{"runtime-config", "publish"}},
		{"rollback without revisions", "", []string{"runtime-config", "rollback"}},
		{"secret create without --value-stdin", "x", []string{"secret", "create", "name"}},
		{"secret rotate without inputs", "", []string{"secret", "rotate", "id"}},
		{"secret rewrap without a key", "", []string{"secret", "rewrap"}},
		{"validate with a missing file", "", []string{"runtime-config", "validate", "--file", filepath.Join(t.TempDir(), "absent.json")}},
		{"validate with invalid JSON", "{bad", []string{"runtime-config", "validate"}},
		{"secret create with an empty value", "\n", []string{"secret", "create", "name", "--value-stdin"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := execute(t, nil, test.stdin, append([]string{"--config", path}, test.args...)...)

			if got.code != clierr.ExitUsage {
				t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, clierr.ExitUsage, got.stderr)
			}
			if !strings.HasPrefix(got.stderr, "Error: ") {
				t.Errorf("stderr = %q", got.stderr)
			}
		})
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.requests) != 0 {
		t.Errorf("invalid commands sent %d requests: %+v", len(server.requests), server.requests)
	}
}

func TestManagementCommandsAgainstARealServer(t *testing.T) {
	t.Run("get authenticates and pretty-prints the document", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(200, `{"revision":3,"config":{"a":1}}`))

		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok-1"), "runtime-config", "get")

		if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, `"revision": 3`) {
			t.Fatalf("result = %+v", got)
		}
		if request := server.last(t); request.Method != http.MethodGet || request.Path != "/api/v1/runtime-config" || request.Auth != "Bearer tok-1" {
			t.Errorf("request = %+v", request)
		}
	})

	t.Run("the server URL and token can come from the environment", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(200, `{"items":[]}`))
		isolate(t)
		t.Setenv("MACHINARCHY_SERVER", server.URL)
		t.Setenv("MACHINARCHY_TOKEN", "env-token")

		var out, errOut bytes.Buffer
		code := cli.Run(context.Background(), cli.Options{
			Streams: cli.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut},
			Groups:  commands.DefaultGroups(),
		}, []string{"--config", writeConfig(t, "http://127.0.0.1:1", "file-token"), "hosts", "-o", "json"})

		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, errOut.String())
		}
		if request := server.last(t); request.Auth != "Bearer env-token" || request.Path != "/api/v1/managed/hosts" {
			t.Errorf("request = %+v", request)
		}
	})

	t.Run("publish sends the precondition and passes a 202 through", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(202, `{"revision":4,"applicationPending":true}`))
		document := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(document, []byte(`{"a":2}`), 0o600); err != nil {
			t.Fatal(err)
		}

		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok"),
			"runtime-config", "publish", "--expected-revision", "3", "--file", document, "--change-note", "why")

		if got.code != 0 || !strings.Contains(got.stdout, `"applicationPending": true`) {
			t.Fatalf("result = %+v", got)
		}

		var body map[string]any
		request := server.last(t)
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatalf("body %q: %v", request.Body, err)
		}
		if request.Method != http.MethodPut || body["expectedRevision"] != float64(3) || body["changeNote"] != "why" {
			t.Errorf("request = %+v, body = %v", request, body)
		}
	})

	t.Run("a stale revision is exit 7 with a hint", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(409, `{"error":"revision conflict"}`))

		got := execute(t, nil, `{"a":1}`, "--config", writeConfig(t, server.URL, "tok"),
			"runtime-config", "publish", "--expected-revision", "1")

		if got.code != clierr.ExitConflict || !strings.Contains(got.stderr, "revision conflict") || !strings.Contains(got.stderr, "Next:  ") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("a non-owner session is exit 3 and the hint explains why", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(403, `{"error":"human owner session required"}`))

		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "mch_pat_token"), "secret", "list")

		if got.code != clierr.ExitAuth {
			t.Fatalf("exit code = %d, stderr %q", got.code, got.stderr)
		}
		if !strings.Contains(got.stderr, "human owner session") || strings.Contains(got.stderr, "mch_pat_token") {
			t.Errorf("stderr = %q", got.stderr)
		}
	})

	t.Run("an expired token is exit 3", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(401, `{"error":"token expired"}`))

		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok"), "runtime-config", "get")
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "Next:  machinarchy login") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("secret create never echoes the value and keeps its default purpose", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(201, `{"id":"s1","name":"db","currentVersion":1}`))

		got := execute(t, nil, "s3cr3t-value\n", "--config", writeConfig(t, server.URL, "tok"),
			"secret", "create", "db", "--value-stdin", "--description", "primary")

		if got.code != 0 || strings.Contains(got.stdout+got.stderr, "s3cr3t-value") {
			t.Fatalf("result = %+v", got)
		}

		var body map[string]any
		if err := json.Unmarshal(server.last(t).Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["value"] != "s3cr3t-value" || body["purpose"] != "ssh-credential" || body["name"] != "db" {
			t.Errorf("body = %v", body)
		}
	})

	t.Run("secret list and create print a table unless -o asks for a document", func(t *testing.T) {
		list := `[{"id":"s1","name":"db","purpose":"ssh-credential","currentVersion":2,"updatedAt":"2026-11-25T10:00:00Z"}]`
		server := newAPIServer(t, jsonReply(200, list))
		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok"), "secret", "list")
		lines := strings.Split(strings.TrimSpace(got.stdout), "\n")
		if got.code != 0 || len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "NAME ID PURPOSE VERSION UPDATED" ||
			strings.Join(strings.Fields(lines[1]), " ") != "db s1 ssh-credential 2 2026-11-25T10:00:00Z" {
			t.Fatalf("table = %+v", got)
		}
		asJSON := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok"), "secret", "list", "-o", "json")
		var decoded []map[string]any
		if err := json.Unmarshal([]byte(asJSON.stdout), &decoded); err != nil || len(decoded) != 1 || decoded[0]["name"] != "db" {
			t.Fatalf("-o json = %q (%v)", asJSON.stdout, err)
		}
		empty := newAPIServer(t, jsonReply(200, `[]`))
		if got := execute(t, nil, "", "--config", writeConfig(t, empty.URL, "tok"), "secret", "list"); got.stdout != "No resources found.\n" {
			t.Fatalf("empty list = %q", got.stdout)
		}
		created := newAPIServer(t, jsonReply(201, `{"id":"s9","name":"key","currentVersion":1}`))
		got = execute(t, nil, "v\n", "--config", writeConfig(t, created.URL, "tok"), "secret", "create", "key", "--value-stdin")
		if got.code != 0 || got.stdout != "Secret \"key\" created: s9 (version 1).\n" {
			t.Fatalf("create = %+v", got)
		}
	})

	t.Run("secret delete handles 204 and 404", func(t *testing.T) {
		ok := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

		got := execute(t, nil, "", "--config", writeConfig(t, ok.URL, "tok"), "secret", "delete", "s1", "--yes")
		if got.code != 0 || got.stdout != "Secret retired.\n" {
			t.Fatalf("result = %+v", got)
		}
		if request := ok.last(t); request.Method != http.MethodDelete || request.Path != "/api/v1/secrets/s1" {
			t.Errorf("request = %+v", request)
		}

		missing := newAPIServer(t, jsonReply(404, `{"error":"secret not found"}`))
		got = execute(t, nil, "", "--config", writeConfig(t, missing.URL, "tok"), "secret", "delete", "nope", "--yes")
		if got.code != clierr.ExitNotFound {
			t.Fatalf("exit code = %d, stderr %q", got.code, got.stderr)
		}
	})

	// AUDIT F-04: destructive commands ask first; without a terminal they
	// refuse unless --yes is given, and nothing reaches the server.
	t.Run("destructive commands require confirmation", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(200, `{}`))
		path := writeConfig(t, server.URL, "tok")
		for _, args := range [][]string{
			{"secret", "delete", "s1"},
			{"secret", "rewrap", "--old-key-id", "k0"},
			{"runtime-config", "rollback", "--expected-revision", "3", "--target-revision", "1"},
		} {
			for _, extra := range [][]string{nil, {"--no-input"}} {
				got := execute(t, nil, "", append(append([]string{"--config", path}, args...), extra...)...)
				if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "--yes") {
					t.Fatalf("%v %v: code=%d stderr=%q, want usage error naming --yes", args, extra, got.code, got.stderr)
				}
			}
		}
		if n := server.count(); n != 0 {
			t.Fatalf("%d requests reached the server without confirmation", n)
		}
	})

	t.Run("an unreachable server is exit 4", func(t *testing.T) {
		server := newAPIServer(t, jsonReply(200, `{}`))
		address := server.URL
		server.Close()

		got := execute(t, nil, "", "--config", writeConfig(t, address, "tok"), "runtime-config", "get")

		if got.code != clierr.ExitUnavailable || !strings.Contains(got.stderr, "cannot reach the API server") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("an HTML page instead of the API is exit 4 with a hint", func(t *testing.T) {
		server := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>sign in</html>")) })

		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok"), "runtime-config", "get")
		if got.code != clierr.ExitUnavailable || !strings.Contains(got.stderr, "Machinarchy API") {
			t.Fatalf("result = %+v", got)
		}
	})
}

func TestNoCommandErrorLeaksTheToken(t *testing.T) {
	const token = "tok-must-never-appear"

	for _, status := range []int{400, 401, 403, 404, 409, 500, 503} {
		server := newAPIServer(t, jsonReply(status, `{"error":"failed"}`))

		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, token), "-v", "runtime-config", "get")

		if strings.Contains(got.stdout+got.stderr, token) {
			t.Errorf("status %d leaked the token:\n%s%s", status, got.stdout, got.stderr)
		}
	}

	unreachable := newAPIServer(t, jsonReply(200, `{}`))
	address := unreachable.URL
	unreachable.Close()
	got := execute(t, nil, "", "--config", writeConfig(t, address, token), "-v", "hosts")
	if strings.Contains(got.stdout+got.stderr, token) {
		t.Errorf("a network failure leaked the token:\n%s%s", got.stdout, got.stderr)
	}
}
