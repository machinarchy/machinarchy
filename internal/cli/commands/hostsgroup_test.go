package commands_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

func hostJSON(id, name string, env int, profile, phase, ready, reason string, labels string) string {
	conditions := "[]"
	if ready != "" {
		conditions = fmt.Sprintf(`[{"type":"Ready","status":%q,"reason":%q}]`, ready, reason)
	}
	return fmt.Sprintf(`{"metadata":{"id":%q,"name":%q,"environmentId":%d,"revision":4,"labels":%s},`+
		`"spec":{"accessProfileId":%q,"accessTarget":{"endpoint":"%s.example:22","trust":["SHA256:trusted"]}},`+
		`"status":{"phase":%q,"conditions":%s,"observedHostKeys":[{"algorithm":"ssh-ed25519","fingerprint":"SHA256:trusted"},{"algorithm":"ssh-rsa","fingerprint":"SHA256:other"}]}}`,
		id, name, env, labels, profile, name, phase, conditions)
}

func hostsGroupServer(t *testing.T) *apiServer {
	t.Helper()
	hosts := strings.Join([]string{
		hostJSON("h1", "web-01", 1, "prof-2", "Active", "True", "Ready", `{"tier":"web"}`),
		hostJSON("h2", "web-02", 1, "prof-2", "Active", "False", "HostKeyPendingApproval", `{}`),
		hostJSON("h3", "db-01", 2, "prof-1", "Active", "False", "ConnectionFailed", `{}`),
		hostJSON("h4", "odd-01", 2, "prof-1", "Active", "False", "Weird", `{}`),
		hostJSON("h5", "chg-01", 2, "prof-1", "Active", "False", "HostKeyChanged", `{}`),
		hostJSON("h6", "new-01", 2, "prof-1", "Pending", "", "", `{}`),
	}, ",")
	routes := map[string]string{
		"GET /api/v1/environments":                   `[{"id":1,"name":"production","description":""},{"id":2,"name":"staging","description":""}]`,
		"POST /api/v1/environments":                  `{"id":3,"name":"x","description":""}`,
		"GET /api/v1/managed/hosts":                  `{"items":[` + hosts + `]}`,
		"GET /api/v1/managed/hosts/h1":               hostJSON("h1", "web-01", 1, "prof-2", "Active", "True", "Ready", `{"tier":"web"}`),
		"PATCH /api/v1/managed/hosts/h1/labels":      hostJSON("h1", "web-01", 1, "prof-2", "Active", "True", "Ready", `{"tier":"web","zone":"a"}`),
		"DELETE /api/v1/managed/hosts/h1":            ``,
		"GET /api/v1/managed/operations":             `{"items":[{"id":"op-1","state":"Succeeded","createdAt":"2026-11-26T10:00:00Z","targets":{}}]}`,
		"GET /api/v1/managed/access-profiles/prof-1": `{"id":"prof-1","name":"agents","currentRevision":1,"spec":{"connectorKind":"agent"}}`,
		"GET /api/v1/managed/access-profiles/prof-2": `{"id":"prof-2","name":"deploy","currentRevision":1,"spec":{"connectorKind":"ssh"}}`,
	}
	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no route","code":"not_found"}`))

			return
		}
		if body == "" {
			w.WriteHeader(http.StatusNoContent)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func TestHostsListMapsReadiness(t *testing.T) {
	server := hostsGroupServer(t)
	config := writeConfig(t, server.URL, "tok")

	got := execute(t, nil, "", "--config", config, "hosts")
	if got.code != 0 {
		t.Fatalf("hosts = %+v", got)
	}
	want := map[string]string{
		"web-01": "ready", "web-02": "waiting for key approval", "db-01": "cannot connect",
		"odd-01": "needs attention", "chg-01": "key changed", "new-01": "checking",
	}
	for host, status := range want {
		found := false
		for _, line := range dataLines(got.stdout) {
			if strings.HasPrefix(line, host) {
				found = strings.Contains(line, status)
			}
		}
		if !found {
			t.Fatalf("%s is not %q in:\n%s", host, status, got.stdout)
		}
	}
	if !strings.Contains(got.stdout, "tier=web") || !strings.Contains(got.stdout, "production") || !strings.Contains(got.stdout, "ssh") ||
		!strings.Contains(got.stdout, "agent") {
		t.Fatalf("columns missing:\n%s", got.stdout)
	}

	ready := execute(t, nil, "", "--config", config, "hosts", "--status", "ready", "-q")
	if strings.TrimSpace(ready.stdout) != "web-01" {
		t.Fatalf("--status ready = %+v", ready)
	}
	attention := execute(t, nil, "", "--config", config, "hosts", "--status", "attention", "-q")
	if len(dataLines(attention.stdout)) != 5 || strings.Contains(attention.stdout, "web-01") {
		t.Fatalf("--status attention = %+v", attention)
	}
	if bad := execute(t, nil, "", "--config", config, "hosts", "--status", "fine"); bad.code != clierr.ExitUsage {
		t.Fatalf("bad --status = %+v", bad)
	}
	if doc := execute(t, nil, "", "--config", config, "hosts", "-o", "json"); !strings.Contains(doc.stdout, `"metadata"`) {
		t.Fatalf("-o json keeps API names: %+v", doc)
	}
}

func TestHostsShowSeparatesTheThings(t *testing.T) {
	server := hostsGroupServer(t)
	config := writeConfig(t, server.URL, "tok")

	got := execute(t, nil, "", "--config", config, "hosts", "show", "web-01")
	for _, want := range []string{"Status:", "ready", "production", "web-01.example:22", "ssh", "tier=web",
		"ssh-ed25519 SHA256:trusted (trusted)", "ssh-rsa SHA256:other (not trusted)", "op-1 Succeeded"} {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("show lacks %q:\n%s", want, got.stdout)
		}
	}
	if missing := execute(t, nil, "", "--config", config, "hosts", "show", "nope"); missing.code != clierr.ExitNotFound {
		t.Fatalf("show unknown = %+v", missing)
	}
}

func TestHostsTagValidatesAndSendsTheWholeSet(t *testing.T) {
	server := hostsGroupServer(t)
	config := writeConfig(t, server.URL, "tok")

	got := execute(t, nil, "", "--config", config, "hosts", "tag", "web-01", "zone=a", "tier-")
	body := server.body(http.MethodPatch, "/api/v1/managed/hosts/h1/labels")
	if got.code != 0 || !strings.Contains(body, `"zone":"a"`) || strings.Contains(body, `"tier"`) {
		t.Fatalf("tag = %+v, body %s", got, body)
	}
	for _, args := range [][]string{{"name=x"}, {"bad key=1"}, {"k=-bad"}, {"a=1", "a-"}, {"nokeyvalue"}} {
		bad := execute(t, nil, "", append([]string{"--config", config, "hosts", "tag", "web-01"}, args...)...)
		if bad.code != clierr.ExitUsage {
			t.Fatalf("tag %v = %+v", args, bad)
		}
	}
}

func TestHostsRemoveIsInventoryOnlyAndGuarded(t *testing.T) {
	server := hostsGroupServer(t)
	config := writeConfig(t, server.URL, "tok")

	if got := execute(t, nil, "", "--config", config, "hosts", "remove", "web-01", "--yes"); got.code != clierr.ExitUsage || server.saw(http.MethodDelete, "/api/v1/managed/hosts/h1") {
		t.Fatalf("--yes alone = %+v", got)
	}
	got := execute(t, nil, "", "--config", config, "hosts", "remove", "web-01", "--confirm", "web-01")
	if got.code != 0 || !server.saw(http.MethodDelete, "/api/v1/managed/hosts/h1") ||
		!strings.Contains(got.stdout, "Not done") || !strings.Contains(got.stdout, "machine was not touched") {
		t.Fatalf("remove = %+v", got)
	}
}

func TestHostsInProtectedEnvNeedTheEnvName(t *testing.T) {
	server := hostsGroupServer(t)
	config := writeConfig(t, server.URL, "tok")
	if got := execute(t, nil, "", "--config", config, "envs", "add", "production", "--protected", "--yes"); got.code != 0 {
		t.Fatalf("envs add = %+v", got)
	}

	if got := execute(t, nil, "", "--config", config, "hosts", "remove", "web-01", "--confirm", "web-01"); got.code != clierr.ExitUsage ||
		server.saw(http.MethodDelete, "/api/v1/managed/hosts/h1") {
		t.Fatalf("protected remove without --confirm-env = %+v", got)
	}
	if got := execute(t, nil, "", "--config", config, "hosts", "tag", "web-01", "a=b"); got.code != clierr.ExitUsage ||
		server.saw(http.MethodPatch, "/api/v1/managed/hosts/h1/labels") {
		t.Fatalf("protected tag without --confirm-env = %+v", got)
	}
	if got := execute(t, nil, "", "--config", config, "hosts", "remove", "web-01", "--confirm", "web-01", "--confirm-env", "production"); got.code != 0 {
		t.Fatalf("protected remove with both = %+v", got)
	}
	// An unprotected env is not asked.
	if got := execute(t, nil, "", "--config", config, "hosts", "remove", "db-01", "--confirm", "db-01"); got.code == clierr.ExitUsage &&
		strings.Contains(got.stderr, "protected") {
		t.Fatalf("unprotected env asked for the env name: %+v", got)
	}
}

func TestHostsMoveIsRefusedUntilTheServerCan(t *testing.T) {
	server := hostsGroupServer(t)
	config := writeConfig(t, server.URL, "tok")

	got := execute(t, nil, "", "--config", config, "hosts", "move", "web-01", "--env", "staging")
	if got.code == 0 || !strings.Contains(got.stderr, "cannot move hosts between envs") || server.saw(http.MethodPatch, "/api/v1/managed/hosts/h1") {
		t.Fatalf("move = %+v", got)
	}
	if noEnv := execute(t, nil, "", "--config", config, "hosts", "move", "web-01"); noEnv.code != clierr.ExitUsage {
		t.Fatalf("move without --env = %+v", noEnv)
	}
}
