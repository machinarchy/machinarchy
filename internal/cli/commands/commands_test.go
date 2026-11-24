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
		"MACHINARCHY_CLI_CONTEXT", "MACHINARCHY_CLI_ENV", "MACHINARCHY_CLI_OUTPUT",
		"MACHINARCHY_CLI_VERBOSE", "MACHINARCHY_CLI_SERVER", "MACHINARCHY_CLI_TOKEN",
		"MACHINARCHY_CLI_REQUEST_TIMEOUT", "MACHINARCHY_CLI_QUIET", "MACHINARCHY_CLI_NO_HEADERS",
		"MACHINARCHY_CLI_NO_INPUT", "MACHINARCHY_CLI_YES",
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

	content := fmt.Sprintf(`apiVersion: machinarchy.io/v1
current-context: test
clusters:
  test:
    server: %s
users:
  test:
    token: %s
contexts:
  test:
    cluster: test
    user: test
`, server, token)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// fakeClient is an in-memory managed API. ListHosts filters by environment
// and simple KEY=VALUE selectors server-side, like the real API, and pages
// pageSize hosts at a time when pageSize is set.
type fakeClient struct {
	hosts    []api.Host
	envs     []api.Environment
	profiles []api.AccessProfile
	ops      map[string]*api.Operation
	err      error
	pageSize int

	submitted []api.OperationInput
	outputs   map[string]string // "op/host" -> the full stream, served in 6-byte steps
	deleted   []string
	trusted   []string
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
	return nil, &api.APIError{Status: 404, Message: "resource not found"}
}

func (f *fakeClient) CreateHost(_ context.Context, input api.HostInput) (*api.Host, error) {
	host := api.Host{Metadata: api.HostMetadata{ID: fmt.Sprint(len(f.hosts) + 100), Name: input.Name, Labels: input.Labels, Revision: 1}, Spec: input.Spec}
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
				return nil, &api.APIError{Status: 409, Message: "resource revision conflict"}
			}
			if len(f.hosts[i].Spec.AccessTarget.Trust) > 0 && !replace {
				return nil, &api.APIError{Status: 409, Message: "the Host already trusts host keys"}
			}
			f.hosts[i].Spec.AccessTarget.Trust = fingerprints
			f.hosts[i].Metadata.Generation++
			f.trusted = append(f.trusted, id+"="+strings.Join(fingerprints, ","))
			return &f.hosts[i], nil
		}
	}
	return nil, &api.APIError{Status: 404, Message: "resource not found"}
}

func (f *fakeClient) SetHostLabels(_ context.Context, id string, revision uint64, labels map[string]string) (*api.Host, error) {
	for i := range f.hosts {
		if f.hosts[i].Metadata.ID == id {
			if f.hosts[i].Metadata.Revision != revision {
				return nil, &api.APIError{Status: 409, Message: "resource revision conflict"}
			}
			f.hosts[i].Metadata.Labels = labels
			f.hosts[i].Metadata.Revision++
			return &f.hosts[i], nil
		}
	}
	return nil, &api.APIError{Status: 404, Message: "resource not found"}
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
		return nil, &api.APIError{Status: 404, Message: "resource not found"}
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
		return nil, &api.APIError{Status: 404, Message: "resource not found"}
	}
	end := min(int(from)+6, len(text))
	return &api.TargetOutput{OperationID: operationID, HostID: hostID, Stream: stream, Text: text[from:end],
		From: from, Next: int64(end), Bytes: int64(len(text)), Live: end < len(text), State: "Running"}, nil
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

func names(lines []string) []string {
	var found []string
	for _, line := range lines {
		if fields := strings.Fields(line); len(fields) > 0 {
			found = append(found, fields[0])
		}
	}

	return found
}

// ---- registration and compatibility ---------------------------------------

func TestCommandTreeKeepsEveryExistingCommand(t *testing.T) {
	isolate(t)
	root := cli.NewRootCommand(cli.Options{Groups: commands.DefaultGroups()})

	want := map[string][]string{
		"get":            {"hosts", "operations", "profiles"},
		"create":         {"host"},
		"delete":         {"host"},
		"label":          {"host"},
		"trust":          {"host"},
		"run":            nil,
		"cancel":         {"operation"},
		"logs":           {"operation"},
		"config":         {"current-context", "get-contexts", "set-cluster", "set-context", "set-credentials", "use-context"},
		"runtime-config": {"get", "history", "publish", "rollback", "status", "validate"},
		"secret":         {"create", "delete", "list", "rewrap", "rotate", "versions"},
		"version":        nil,
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

	if command, _, err := root.Find([]string{"get", "host"}); err != nil || command.Name() != "hosts" {
		t.Errorf("the \"host\" alias no longer resolves: %v", err)
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

func TestGetHostsHelpHasExamples(t *testing.T) {
	got := execute(t, nil, "", "get", "hosts", "--help")

	if !strings.Contains(got.stdout, "Examples:") || !strings.Contains(got.stdout, "machinarchy get hosts") {
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

// ---- get hosts -------------------------------------------------------------

func TestGetHosts(t *testing.T) {
	cfg := writeConfig(t, "http://127.0.0.1:1", "tok")
	run := func(t *testing.T, client *fakeClient, args ...string) result {
		t.Helper()

		return execute(t, fake(client), "", append([]string{"--config", cfg}, args...)...)
	}

	t.Run("lists hosts as a table", func(t *testing.T) {
		got := run(t, fixtures(), "get", "hosts")

		if got.code != 0 || got.stderr != "" {
			t.Fatalf("result = %+v", got)
		}
		lines := dataLines(got.stdout)
		if len(lines) != 6 || !strings.HasPrefix(lines[0], "NAME") {
			t.Fatalf("output:\n%s", got.stdout)
		}
		if first := strings.Fields(lines[1]); strings.Join(first, "|") != "web-01|1|1|10.0.0.1:22|Active|-" {
			t.Errorf("first row = %v", first)
		}
	})

	t.Run("json", func(t *testing.T) {
		got := run(t, fixtures(), "get", "hosts", "-o", "json")

		var decoded []map[string]any
		if err := json.Unmarshal([]byte(got.stdout), &decoded); err != nil || len(decoded) != 5 {
			t.Fatalf("err = %v, hosts = %d\n%s", err, len(decoded), got.stdout)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		got := run(t, fixtures(), "get", "hosts", "-o", "yaml")
		if got.code != 0 || !strings.Contains(got.stdout, "name: web-01") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("one host by name and by id through the alias", func(t *testing.T) {
		byName := run(t, fixtures(), "get", "hosts", "web-01")
		byID := run(t, fixtures(), "get", "host", "3")

		if got := names(dataLines(byName.stdout)); strings.Join(got, ",") != "NAME,web-01" {
			t.Errorf("by name = %v\n%s", got, byName.stdout)
		}
		if got := names(dataLines(byID.stdout)); strings.Join(got, ",") != "NAME,db-01" {
			t.Errorf("by id = %v\n%s", got, byID.stdout)
		}

		asJSON := run(t, fixtures(), "get", "hosts", "web-01", "-o", "json")
		var object map[string]any
		if err := json.Unmarshal([]byte(asJSON.stdout), &object); err != nil || object["metadata"].(map[string]any)["id"] != "1" {
			t.Errorf("single JSON = %s (%v)", asJSON.stdout, err)
		}
	})

	t.Run("a missing host is exit 6", func(t *testing.T) {
		got := run(t, fixtures(), "get", "hosts", "nope")

		if got.code != clierr.ExitNotFound || !strings.Contains(got.stderr, `host "nope" was not found`) {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("an ambiguous host is a usage error", func(t *testing.T) {
		got := run(t, fixtures(), "get", "hosts", "dup")

		if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "is ambiguous") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("environment filter by name, shorthand and id", func(t *testing.T) {
		for _, args := range [][]string{
			{"get", "hosts", "--env", "prod"},
			{"get", "hosts", "-n", "PROD"},
			{"get", "hosts", "--env", "1"},
		} {
			got := run(t, fixtures(), args...)
			if gotNames := names(dataLines(got.stdout)); strings.Join(gotNames, ",") != "NAME,web-01,web-02,dup" {
				t.Errorf("%v = %v\n%s", args, gotNames, got.stdout)
			}
		}
	})

	t.Run("an environment narrows an otherwise ambiguous name", func(t *testing.T) {
		got := run(t, fixtures(), "get", "hosts", "dup", "--env", "dev", "-q")
		if got.code != 0 || got.stdout != "5\n" {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("an unknown environment is exit 6", func(t *testing.T) {
		got := run(t, fixtures(), "get", "hosts", "--env", "staging")

		if got.code != clierr.ExitNotFound || !strings.Contains(got.stderr, `environment "staging" was not found`) {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("no-headers and quiet", func(t *testing.T) {
		noHeaders := run(t, fixtures(), "get", "hosts", "--no-headers")
		if lines := dataLines(noHeaders.stdout); len(lines) != 5 || strings.HasPrefix(lines[0], "NAME") {
			t.Errorf("--no-headers output:\n%s", noHeaders.stdout)
		}

		quiet := run(t, fixtures(), "get", "hosts", "-q")
		if quiet.stdout != "1\n2\n3\n4\n5\n" {
			t.Errorf("-q output = %q", quiet.stdout)
		}

		quietJSON := run(t, fixtures(), "get", "hosts", "-q", "-o", "json")
		if !strings.HasPrefix(quietJSON.stdout, "[") {
			t.Errorf("-q must not alter JSON documents: %q", quietJSON.stdout)
		}
	})

	t.Run("an empty inventory keeps the original table message", func(t *testing.T) {
		got := run(t, &fakeClient{}, "get", "hosts")

		if got.code != 0 || !strings.Contains(got.stdout, "No resources found.") {
			t.Fatalf("result = %+v", got)
		}

		asJSON := run(t, &fakeClient{}, "get", "hosts", "-o", "json")
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

		for _, args := range [][]string{
			{"get", "hosts", "-o", "xml"},
			{"get", "hosts", "a", "b"},
			{"get", "nope"},
		} {
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
			{"unauthorized", &api.APIError{Status: 401, Message: "expired"}, clierr.ExitAuth, "set-credentials"},
			{"forbidden", &api.APIError{Status: 403, Message: "no"}, clierr.ExitAuth, "permitted"},
			{"server error", &api.APIError{Status: 502, Message: "bad gateway"}, clierr.ExitUnavailable, "Retry later"},
			{"unreachable", clierr.Newf(clierr.ExitUnavailable, "check the URL", "cannot reach the API server"), clierr.ExitUnavailable, "check the URL"},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				got := run(t, &fakeClient{err: test.err}, "get", "hosts")

				if got.code != test.wantCode {
					t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, test.wantCode, got.stderr)
				}
				if !strings.Contains(got.stderr, "Error: list hosts: ") || !strings.Contains(got.stderr, test.wantHint) {
					t.Errorf("stderr = %q", got.stderr)
				}
				if got.stdout != "" {
					t.Errorf("stdout = %q, want nothing", got.stdout)
				}
			})
		}
	})

	t.Run("a missing context explains how to set one up", func(t *testing.T) {
		got := execute(t, fake(fixtures()), "", "--config", filepath.Join(t.TempDir(), "absent.yaml"), "get", "hosts")

		if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "config set-cluster") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("the get group prints help", func(t *testing.T) {
		got := execute(t, nil, "", "get")
		if got.code != 0 || !strings.Contains(got.stdout, "hosts") {
			t.Fatalf("result = %+v", got)
		}
	})
}

// ---- config commands -------------------------------------------------------

func TestConfigCommandsEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	step := func(stdin string, args ...string) result {
		t.Helper()

		return execute(t, nil, stdin, append([]string{"--config", path}, args...)...)
	}

	// Mixed case and dots are preserved exactly: the config document is parsed
	// as YAML, not through Viper's case-folding key handling.
	for _, command := range []struct {
		stdin string
		args  []string
	}{
		{"", []string{"config", "set-cluster", "Prod.EU", "--server", "https://api.example.com/"}},
		{"", []string{"config", "set-cluster", "Lab", "--server", "http://lab.example.test", "--insecure-http"}},
		{"tok-abc123\n", []string{"config", "set-credentials", "Ops.User", "--token-stdin"}},
		{"", []string{"config", "set-context", "Prod.EU", "--cluster", "Prod.EU", "--user", "Ops.User", "--env", "Staging"}},
		{"", []string{"config", "use-context", "Prod.EU"}},
	} {
		if got := step(command.stdin, command.args...); got.code != 0 {
			t.Fatalf("%v: %+v", command.args, got)
		}
	}

	t.Run("credentials are never echoed", func(t *testing.T) {
		got := step("tok-abc123\n", "config", "set-credentials", "Again", "--token-stdin")

		if got.code != 0 || strings.Contains(got.stdout+got.stderr, "tok-abc123") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("current context", func(t *testing.T) {
		if got := step("", "config", "current-context"); got.code != 0 || got.stdout != "Prod.EU\n" {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("get contexts preserves the exact names", func(t *testing.T) {
		got := step("", "config", "get-contexts")

		for _, want := range []string{"Prod.EU", "Ops.User", "Staging", "*"} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("output lacks %q:\n%s", want, got.stdout)
			}
		}
		if strings.Contains(got.stdout, "prod.eu") {
			t.Errorf("a context name was case-folded:\n%s", got.stdout)
		}
	})

	t.Run("the file is owner-only and keeps the original spelling", func(t *testing.T) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "Prod.EU:") || !strings.Contains(string(data), "https://api.example.com") {
			t.Errorf("config file:\n%s", data)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode := info.Mode().Perm(); mode != 0o600 {
				t.Errorf("config file mode = %04o, want 0600", mode)
			}
		}
	})

	t.Run("a command resolves the saved context", func(t *testing.T) {
		var target config.Target
		builder := func(resolved config.Target) (api.Client, error) {
			target = resolved
			return &fakeClient{envs: []api.Environment{{ID: 1, Name: "Staging"}}}, nil
		}

		got := execute(t, builder, "", "--config", path, "get", "hosts", "--env", "")
		if got.code != 0 {
			t.Fatalf("result = %+v", got)
		}
		if target.ContextName != "Prod.EU" || target.Server != "https://api.example.com" || target.Token != "tok-abc123" || target.Environment != "Staging" {
			t.Fatalf("target = %+v", target)
		}
	})
}

func TestConfigCommandErrorsAreUsageErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	tests := []struct {
		name    string
		stdin   string
		args    []string
		message string
	}{
		{"set-cluster without server", "", []string{"config", "set-cluster", "x"}, "--server is required"},
		{"set-cluster with a bad URL", "", []string{"config", "set-cluster", "x", "--server", "localhost"}, "absolute http:// or https:// URL"},
		{"set-cluster with remote plain http", "", []string{"config", "set-cluster", "x", "--server", "http://api.example.test"}, "--insecure-http"},
		{"use-context unknown", "", []string{"config", "use-context", "nope"}, `context "nope" does not exist`},
		{"current-context unset", "", []string{"config", "current-context"}, "no current context is selected"},
		{"set-credentials without a source", "", []string{"config", "set-credentials", "x"}, "provide exactly one of"},
		{"set-credentials with both sources", "t", []string{"config", "set-credentials", "x", "--token", "t", "--token-stdin"}, "provide exactly one of"},
		{"set-credentials with an invalid token", "has space", []string{"config", "set-credentials", "x", "--token-stdin"}, "API token is invalid"},
		{"set-context referencing nothing", "", []string{"config", "set-context", "x", "--cluster", "c", "--user", "u"}, `cluster "c" does not exist`},
		{"set-context without references", "", []string{"config", "set-context", "x"}, "--cluster and --user are required"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := execute(t, nil, test.stdin, append([]string{"--config", path}, test.args...)...)

			if got.code != clierr.ExitUsage {
				t.Fatalf("exit code = %d, want %d (stderr %q)", got.code, clierr.ExitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, test.message) {
				t.Errorf("stderr = %q, want %q", got.stderr, test.message)
			}
			if strings.Contains(got.stderr, "has space") {
				t.Errorf("the rejected token leaked: %q", got.stderr)
			}
		})
	}

	if _, err := os.Stat(path); err == nil {
		t.Error("a failed command created a config file")
	}
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
		server := newAPIServer(t, jsonReply(200, `{"revision":1}`))
		isolate(t)
		t.Setenv("MACHINARCHY_CLI_SERVER", server.URL)
		t.Setenv("MACHINARCHY_CLI_TOKEN", "env-token")

		var out, errOut bytes.Buffer
		code := cli.Run(context.Background(), cli.Options{
			Streams: cli.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut},
			Groups:  commands.DefaultGroups(),
		}, []string{"--config", writeConfig(t, "http://127.0.0.1:1", "file-token"), "runtime-config", "status"})

		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, errOut.String())
		}
		if request := server.last(t); request.Auth != "Bearer env-token" || request.Path != "/api/v1/runtime-config/status" {
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

		if got.code != clierr.ExitConflict || !strings.Contains(got.stderr, "revision conflict") || !strings.Contains(got.stderr, "Hint: ") {
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
		if got.code != clierr.ExitAuth || !strings.Contains(got.stderr, "set-credentials") {
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
	got := execute(t, nil, "", "--config", writeConfig(t, address, token), "-v", "get", "hosts")
	if strings.Contains(got.stdout+got.stderr, token) {
		t.Errorf("a network failure leaked the token:\n%s%s", got.stdout, got.stderr)
	}
}

// ---- managed host and operation commands (B8b) -----------------------------

func TestManagedCommands(t *testing.T) {
	cfg := writeConfig(t, "http://127.0.0.1:1", "tok")
	run := func(t *testing.T, client *fakeClient, args ...string) result {
		t.Helper()
		return execute(t, fake(client), "", append([]string{"--config", cfg}, args...)...)
	}

	t.Run("get hosts follows every page and passes the selector", func(t *testing.T) {
		client := fixtures()
		client.pageSize = 2
		got := run(t, client, "get", "hosts", "-q")
		if got.stdout != "1\n2\n3\n4\n5\n" || client.calls != 3 {
			t.Fatalf("stdout = %q after %d calls", got.stdout, client.calls)
		}
		got = run(t, fixtures(), "get", "hosts", "-l", "tier=web", "-q")
		if got.stdout != "1\n2\n" {
			t.Fatalf("selector stdout = %q", got.stdout)
		}
	})

	t.Run("create host resolves the profile by name", func(t *testing.T) {
		client := fixtures()
		got := run(t, client, "create", "host", "web-09", "--endpoint", "10.0.0.9:22", "--profile", "deploy", "--label", "tier=web", "-q")
		if got.code != 0 || got.stdout != "105\n" {
			t.Fatalf("result = %+v", got)
		}
		created := client.hosts[len(client.hosts)-1]
		if created.Spec.AccessProfileID != "p-1" || created.Spec.AccessTarget.Endpoint != "10.0.0.9:22" || created.Metadata.Labels["tier"] != "web" {
			t.Fatalf("created = %+v", created)
		}
		missing := run(t, fixtures(), "create", "host", "x", "--endpoint", "h:22", "--profile", "nope")
		if missing.code != clierr.ExitNotFound {
			t.Fatalf("unknown profile = %+v", missing)
		}
		usage := run(t, fixtures(), "create", "host", "x", "--endpoint", "h:22")
		if usage.code != clierr.ExitUsage {
			t.Fatalf("missing profile = %+v", usage)
		}
		// A host reached through its agent has no SSH endpoint.
		client = fixtures()
		if got := run(t, client, "create", "host", "agent-01", "--profile", "deploy", "-q"); got.code != 0 {
			t.Fatalf("host without endpoint = %+v", got)
		}
		if created := client.hosts[len(client.hosts)-1]; created.Spec.AccessTarget.Endpoint != "" {
			t.Fatalf("created = %+v", created)
		}
	})

	t.Run("delete host confirms and sends the revision", func(t *testing.T) {
		client := fixtures()
		if got := run(t, client, "delete", "host", "web-01"); got.code != clierr.ExitUsage || len(client.deleted) != 0 {
			t.Fatalf("unconfirmed delete = %+v, deleted %v", got, client.deleted)
		}
		if got := run(t, client, "delete", "host", "web-01", "--yes"); got.code != 0 || strings.Join(client.deleted, ",") != "1@1" {
			t.Fatalf("delete = %+v, deleted %v", got, client.deleted)
		}
	})

	t.Run("label host sets and removes labels", func(t *testing.T) {
		client := fixtures()
		got := run(t, client, "label", "host", "web-01", "canary=yes", "tier-", "-o", "json")
		if got.code != 0 {
			t.Fatalf("result = %+v", got)
		}
		labels := client.hosts[0].Metadata.Labels
		if labels["canary"] != "yes" || labels["tier"] != "" || len(labels) != 1 {
			t.Fatalf("labels = %v", labels)
		}
		if bad := run(t, fixtures(), "label", "host", "web-01", "oops"); bad.code != clierr.ExitUsage {
			t.Fatalf("bad label = %+v", bad)
		}
	})

	t.Run("trust host approves exactly the observed keys after confirmation", func(t *testing.T) {
		const ed, ecdsa = "SHA256:ed25519ed25519ed25519ed25519ed25519ed25", "SHA256:ecdsaecdsaecdsaecdsaecdsaecdsaecdsaecdsaec"
		pending := func(host api.Host) api.Host {
			host.Metadata.Generation = 4
			host.Status.Conditions = []api.Condition{{Type: "Ready", Status: "False", Reason: "HostKeyPendingApproval"}}
			host.Status.ObservedHostKeys = []api.ObservedHostKey{{Algorithm: "ssh-ed25519", Fingerprint: ed}, {Algorithm: "ecdsa-sha2-nistp256", Fingerprint: ecdsa}}
			return host
		}
		client := fixtures()
		client.hosts[0] = pending(client.hosts[0])
		if got := run(t, client, "trust", "host", "web-01"); got.code != clierr.ExitUsage || len(client.trusted) != 0 ||
			!strings.Contains(got.stderr+got.stdout, ed) {
			t.Fatalf("unconfirmed trust = %+v (it must show the keys and change nothing)", got)
		}
		if got := run(t, client, "trust", "host", "web-01", "--yes"); got.code != 0 || strings.Join(client.trusted, ";") != "1="+ed+","+ecdsa {
			t.Fatalf("trust = %+v, trusted %v", got, client.trusted)
		}
		// web-01 now trusts keys: changing them needs --replace.
		if got := run(t, client, "trust", "host", "web-01", "--yes"); got.code != clierr.ExitUsage || len(client.trusted) != 1 ||
			!strings.Contains(got.stderr, "--replace") {
			t.Fatalf("re-trust without --replace = %+v", got)
		}
		if got := run(t, client, "trust", "host", "web-01", "--replace", "--fingerprint", ecdsa, "--yes"); got.code != 0 || client.trusted[1] != "1="+ecdsa {
			t.Fatalf("replace = %+v, trusted %v", got, client.trusted)
		}
		if got := run(t, client, "trust", "host", "web-02", "--yes"); got.code != clierr.ExitUsage {
			t.Fatalf("a host with no observed keys = %+v", got)
		}
	})

	t.Run("trust host --all-pending approves only pending hosts that match", func(t *testing.T) {
		client := fixtures()
		for i := range client.hosts {
			client.hosts[i].Status.Conditions = []api.Condition{{Type: "Ready", Status: "False", Reason: "HostKeyPendingApproval"}}
			client.hosts[i].Status.ObservedHostKeys = []api.ObservedHostKey{{Fingerprint: "SHA256:key-" + client.hosts[i].Metadata.ID}}
		}
		client.hosts[1].Status.Conditions[0].Reason = "SSHVerified" // web-02 is not pending
		if got := run(t, client, "trust", "host", "--all-pending", "--selector", "tier=web", "--yes"); got.code != 0 {
			t.Fatalf("all-pending = %+v", got)
		}
		if strings.Join(client.trusted, ";") != "1=SHA256:key-1" {
			t.Fatalf("trusted %v, want only the pending web host", client.trusted)
		}
		for _, args := range [][]string{{"trust", "host"}, {"trust", "host", "web-01", "--all-pending"}, {"trust", "host", "--all-pending", "--replace"}} {
			if got := run(t, client, args...); got.code != clierr.ExitUsage {
				t.Errorf("%v = %+v, want a usage error", args, got)
			}
		}
	})

	t.Run("run previews, confirms, and binds to the preview", func(t *testing.T) {
		client := fixtures()
		if got := run(t, client, "run", "-l", "tier=web", "--", "uptime"); got.code != clierr.ExitUsage || len(client.submitted) != 0 {
			t.Fatalf("unconfirmed run = %+v", got)
		}
		got := run(t, client, "run", "-l", "tier=web", "--yes", "--", "uptime", "-p")
		if got.code != 0 || !strings.Contains(got.stdout, "op-1") {
			t.Fatalf("run = %+v", got)
		}
		submitted := client.submitted[0]
		if submitted.ExpectTargetsHash != "hash-2" || strings.Join(submitted.Run.Argv, " ") != "uptime -p" || submitted.Targets.Selector != "tier=web" {
			t.Fatalf("submitted = %+v", submitted)
		}
		for _, args := range [][]string{
			{"run", "--yes", "--", "uptime"},    // no selection
			{"run", "-l", "tier=web", "uptime"}, // program not after --
			{"run", "--host", "ghost", "--yes", "--", "uptime"},
		} {
			if got := run(t, fixtures(), args...); got.code == 0 {
				t.Errorf("%v succeeded: %+v", args, got)
			}
		}
	})

	t.Run("run --wait prints per-host results", func(t *testing.T) {
		got := run(t, fixtures(), "run", "--host", "web-01", "--yes", "--wait", "--", "uptime")
		if got.code != 0 || !strings.Contains(got.stdout, "web-01") || !strings.Contains(got.stdout, "up 3 days") {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("run detaches unless it waits for a short command", func(t *testing.T) {
		client := fixtures()
		got := run(t, client, "run", "-l", "tier=web", "--yes", "--", "backup.sh")
		if got.code != 0 || !strings.Contains(got.stdout, "logs operation op-1 --follow") {
			t.Fatalf("detached run = %+v", got)
		}
		if spec := client.submitted[0].Run; !spec.Detach || spec.Concurrency < 1 {
			t.Fatalf("run without --wait submitted %+v, want detached", spec)
		}
		client = fixtures()
		run(t, client, "run", "--host", "web-01", "--yes", "--wait", "--", "uptime")
		if spec := client.submitted[0].Run; spec.Detach || spec.Concurrency != 1 {
			t.Fatalf("run --wait submitted %+v, want attached", spec)
		}
		for _, args := range [][]string{
			{"run", "--host", "web-01", "--yes", "--wait", "--timeout", "3600", "--", "uptime"},
			{"run", "--host", "web-01", "--yes", "--wait", "--detach", "--", "uptime"},
		} {
			client = fixtures()
			run(t, client, args...)
			if !client.submitted[0].Run.Detach {
				t.Fatalf("%v submitted an attached run", args)
			}
		}
	})

	t.Run("run --capability submits a capability Operation", func(t *testing.T) {
		client := fixtures()
		got := run(t, client, "run", "-l", "tier=web", "--yes", "--capability", "service.status", "--input", `{"service":"nginx.service"}`, "--timeout", "45")
		if got.code != 0 || !strings.Contains(got.stdout, "op-1") {
			t.Fatalf("capability run = %+v", got)
		}
		submitted := client.submitted[0]
		if submitted.Run != nil || submitted.Capability == nil || submitted.Capability.Name != "service.status" ||
			string(submitted.Capability.Input) != `{"service":"nginx.service"}` || submitted.Capability.TimeoutSeconds != 45 || submitted.ExpectTargetsHash != "hash-2" {
			t.Fatalf("submitted = %+v", submitted)
		}
		for _, args := range [][]string{
			{"run", "-l", "tier=web", "--yes", "--capability", "service.status", "--", "uptime"},    // a program as well
			{"run", "-l", "tier=web", "--yes", "--capability", "service.status", "--input", "[1]"},  // input not an object
			{"run", "-l", "tier=web", "--yes", "--capability", "service.status", "--input", "{bad"}, // input not JSON
			{"run", "-l", "tier=web", "--yes", "--capability", "service.status", "--detach"},
			{"run", "--yes", "--capability", "service.status"}, // no selection
		} {
			client := fixtures()
			if got := run(t, client, args...); got.code != clierr.ExitUsage || len(client.submitted) != 0 {
				t.Errorf("%v = %+v, want a usage error", args, got)
			}
		}
	})

	t.Run("logs follows a target's output", func(t *testing.T) {
		client := fixtures()
		zero := 0
		client.ops = map[string]*api.Operation{"op-7": {ID: "op-7", Status: api.OperationStatus{State: "PartiallySucceeded", Targets: []api.OperationTarget{
			{Spec: api.OperationTargetSpec{HostID: "h1", DisplayName: "web-01"}, State: "Running"},
			{Spec: api.OperationTargetSpec{HostID: "h2", DisplayName: "web-02"}, State: "Succeeded", Result: &api.TargetResult{ExitCode: &zero}},
		}}}}
		client.outputs = map[string]string{"op-7/h1/stdout": "line1\nline2\nline3\n", "op-7/h2/stdout": "ok\n", "op-7/h1/stderr": "warn\n"}
		got := run(t, client, "logs", "operation", "op-7", "--host", "web-01", "--follow")
		if got.code != 0 || got.stdout != "line1\nline2\nline3\n" {
			t.Fatalf("follow = %+v", got)
		}
		if got := run(t, client, "logs", "operation", "op-7", "--host", "h1", "--stream", "stderr"); got.stdout != "warn\n" {
			t.Fatalf("stderr by host ID = %+v", got)
		}
		// Without --host every target is printed with a header.
		if got := run(t, client, "logs", "operation", "op-7"); !strings.Contains(got.stdout, "==> web-01 (Running) <==\nline1\n") ||
			!strings.Contains(got.stdout, "==> web-02 (Succeeded) <==\nok\n") {
			t.Fatalf("all targets = %q", got.stdout)
		}
		for _, args := range [][]string{
			{"logs", "operation", "op-7", "--follow"},          // several targets need --host
			{"logs", "operation", "op-7", "--stream", "stdin"}, // bad stream
		} {
			if got := run(t, client, args...); got.code != clierr.ExitUsage {
				t.Errorf("%v = %+v, want a usage error", args, got)
			}
		}
		if got := run(t, client, "logs", "operation", "op-7", "--host", "db-01"); got.code != clierr.ExitNotFound {
			t.Errorf("unknown host = %+v", got)
		}
	})

	t.Run("operations and profiles", func(t *testing.T) {
		if got := run(t, fixtures(), "get", "operations"); !strings.Contains(got.stdout, "op-9") || !strings.Contains(got.stdout, "Succeeded=2") {
			t.Fatalf("operations = %+v", got)
		}
		if got := run(t, fixtures(), "get", "profiles"); !strings.Contains(got.stdout, "deploy") {
			t.Fatalf("profiles = %+v", got)
		}
		if got := run(t, fixtures(), "cancel", "operation", "op-3"); !strings.Contains(got.stdout, "op-3") {
			t.Fatalf("cancel = %+v", got)
		}
	})
}
