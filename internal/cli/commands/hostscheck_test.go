package commands_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

func checkServer(t *testing.T, answer string, status int) *apiServer {
	t.Helper()
	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/environments":
			_, _ = w.Write([]byte(`[{"id":1,"name":"production","description":""}]`))
		case "GET /api/v1/managed/hosts":
			_, _ = w.Write([]byte(`{"items":[` + hostJSON("h1", "web-01", 1, "prof-2", "Active", "True", "Ready", `{}`) + `]}`))
		case "POST /api/v1/managed/hosts/h1/check":
			w.WriteHeader(status)
			_, _ = w.Write([]byte(answer))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no route","code":"not_found"}`))
		}
	})
}

func TestHostsCheckReady(t *testing.T) {
	server := checkServer(t, `{"hostId":"h1","ready":true,"reachable":true,"reason":"SSHVerified","checkedAt":"2026-11-27T10:00:00Z"}`, 200)
	config := writeConfig(t, server.URL, "tok")
	got := execute(t, nil, "", "--config", config, "hosts", "check", "web-01")
	if got.code != 0 || !strings.Contains(got.stdout, "web-01: ready (SSHVerified)") {
		t.Fatalf("check = %+v", got)
	}
	if last := server.last(t); last.Method != http.MethodPost || last.Path != "/api/v1/managed/hosts/h1/check" || len(last.Body) != 0 {
		t.Fatalf("request = %+v", last)
	}
	asJSON := execute(t, nil, "", "--config", config, "hosts", "check", "web-01", "-o", "json")
	if asJSON.code != 0 || !strings.Contains(asJSON.stdout, `"reason": "SSHVerified"`) {
		t.Fatalf("json = %+v", asJSON)
	}
}

// A host that is not ready exits non-zero, and a changed key points at the
// stricter --replace step, never at a command that trusts it.
func TestHostsCheckNotReadyFailsAndNamesTheNextStep(t *testing.T) {
	for reason, next := range map[string]string{
		"HostKeyChanged":         "hosts trust web-01 --replace",
		"HostKeyPendingApproval": "hosts trust web-01",
		"AuthenticationFailed":   "hosts show web-01",
	} {
		server := checkServer(t, `{"hostId":"h1","ready":false,"reachable":true,"reason":"`+reason+`","message":"nothing was sent"}`, 200)
		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok"), "hosts", "check", "web-01")
		if got.code != clierr.ExitFailure || !strings.Contains(got.stdout, "not ready ("+reason+")") || !strings.Contains(got.stderr, next) {
			t.Errorf("%s: %+v", reason, got)
		}
		if strings.Contains(got.stderr, "--yes") {
			t.Errorf("%s: next step must not skip the question: %s", reason, got.stderr)
		}
	}
}

func TestHostsCheckServerRefusals(t *testing.T) {
	for status, want := range map[int]int{403: clierr.ExitAuth, 409: clierr.ExitConflict} {
		server := checkServer(t, `{"error":"no","code":"x"}`, status)
		got := execute(t, nil, "", "--config", writeConfig(t, server.URL, "tok"), "hosts", "check", "web-01")
		if got.code == 0 {
			t.Errorf("status %d: exit 0, want an error (%d)", status, want)
		}
	}
	got := execute(t, nil, "", "--config", writeConfig(t, "http://127.0.0.1:1", "tok"), "hosts", "check")
	if got.code != clierr.ExitUsage {
		t.Errorf("missing NAME: %+v", got)
	}
}
