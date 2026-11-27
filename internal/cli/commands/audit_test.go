package commands_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

// seedAudit gives the fake server n chained events (ids 1..n); brokenAt, when
// non-zero, corrupts that event's prevHash.
func (s *authServer) seedAudit(n int, brokenAt int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = nil
	prev := "genesis"
	for i := int64(1); i <= int64(n); i++ {
		hash := fmt.Sprintf("h%d", i)
		p := prev
		if i == brokenAt {
			p = "tampered"
		}
		action := "member.role_changed"
		if i%2 == 0 {
			action = "host.created"
		}
		s.audit = append(s.audit, map[string]any{"id": i, "occurredAt": "2026-11-27T10:00:00Z", "actorUsername": "maxim",
			"action": action, "targetType": "member", "targetId": strconv.FormatInt(i, 10), "outcome": "success",
			"details": map[string]string{}, "prevHash": p, "hash": hash})
		prev = hash
	}
}

func (s *authServer) auditRoute(r *http.Request, reply func(int, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/api/v1/audit/verify" {
		valid := true
		var first int64
		for i := 1; i < len(s.audit); i++ {
			if s.audit[i]["prevHash"] != s.audit[i-1]["hash"] {
				valid, first = false, int64(i+1)

				break
			}
		}
		head := ""
		if len(s.audit) > 0 {
			head, _ = s.audit[len(s.audit)-1]["hash"].(string)
		}
		body, _ := json.Marshal(map[string]any{"valid": valid, "events": len(s.audit), "headHash": head, "firstInvalidId": first, "reason": "hash link mismatch"})
		reply(200, string(body))

		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before_id"), 10, 64)
	action := r.URL.Query().Get("action")
	out := []map[string]any{}
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.audit[i]
		if before > 0 && e["id"].(int64) >= before {
			continue
		}
		if action != "" && e["action"] != action {
			continue
		}
		out = append(out, e)
	}
	body, _ := json.Marshal(out)
	reply(200, string(body))
}

func auditServer(t *testing.T, n int, brokenAt int64) (*authServer, string) {
	t.Helper()
	server := newAuthServer(t, false)
	server.seedAudit(n, brokenAt)

	return server, ownerConfig(t, server)
}

func TestAuditListAndVerify(t *testing.T) {
	_, path := auditServer(t, 3, 0)

	got := runAuth(t, authRun{config: path}, "audit")
	if got.code != 0 || !strings.Contains(got.stdout, "member.role_changed") || !strings.Contains(got.stdout, "host.created") {
		t.Fatalf("list = %+v", got)
	}
	if bad := runAuth(t, authRun{config: path}, "audit", "--limit", "0"); bad.code != clierr.ExitUsage {
		t.Fatalf("--limit 0 must be a usage error: %+v", bad)
	}
	ok := runAuth(t, authRun{config: path}, "audit", "verify")
	if ok.code != 0 || !strings.Contains(ok.stdout, "intact: 3 events") || !strings.Contains(ok.stdout, "Head hash: h3") {
		t.Fatalf("verify = %+v", ok)
	}
}

func TestAuditVerifyReportsABrokenChain(t *testing.T) {
	_, path := auditServer(t, 3, 2)

	got := runAuth(t, authRun{config: path}, "audit", "verify")
	if got.code == 0 || !strings.Contains(got.stdout, "BROKEN at event 2") {
		t.Fatalf("verify = %+v", got)
	}
}

func TestAuditExportPagesAndSummarises(t *testing.T) {
	_, path := auditServer(t, 1203, 0)

	got := runAuth(t, authRun{config: path}, "audit", "export")
	if got.code != 0 {
		t.Fatalf("export = code %d stderr %s", got.code, got.stderr)
	}
	lines := strings.Split(strings.TrimRight(got.stdout, "\n"), "\n")
	if len(lines) != 1204 {
		t.Fatalf("lines = %d, want 1203 events + summary", len(lines))
	}
	if !strings.Contains(lines[0], `"id":1203`) || !strings.Contains(lines[1202], `"id":1`) {
		t.Fatalf("events must be newest first: %s ... %s", lines[0], lines[1202])
	}
	var last struct {
		Summary struct {
			Events      int    `json:"events"`
			HeadHash    string `json:"headHash"`
			ChainLinked *bool  `json:"chainLinked"`
			Truncated   bool   `json:"truncated"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(lines[1203]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Summary.Events != 1203 || last.Summary.HeadHash != "h1203" || last.Summary.ChainLinked == nil || !*last.Summary.ChainLinked || last.Summary.Truncated {
		t.Fatalf("summary = %+v", last.Summary)
	}
}

func TestAuditExportIsBoundedAndSaysSo(t *testing.T) {
	_, path := auditServer(t, 10, 0)

	got := runAuth(t, authRun{config: path}, "audit", "export", "--max", "4")
	if got.code != 0 || !strings.Contains(got.stdout, `"events":4`) || !strings.Contains(got.stdout, `"truncated":true`) {
		t.Fatalf("export = %+v", got)
	}
	if bad := runAuth(t, authRun{config: path}, "audit", "export", "--max", "0"); bad.code != clierr.ExitUsage {
		t.Fatalf("--max 0 must be a usage error: %+v", bad)
	}
}

func TestAuditExportDetectsABrokenChainAndFilteredSkipsTheCheck(t *testing.T) {
	_, path := auditServer(t, 6, 3)

	got := runAuth(t, authRun{config: path}, "audit", "export")
	if got.code == 0 || !strings.Contains(got.stdout, `"chainLinked":false`) || !strings.Contains(got.stdout, `"brokenAtId":`) {
		t.Fatalf("broken chain export = %+v", got)
	}
	filtered := runAuth(t, authRun{config: path}, "audit", "export", "--action", "host.created")
	if filtered.code != 0 || strings.Contains(filtered.stdout, "chainLinked") || strings.Contains(filtered.stdout, "member.role_changed") {
		t.Fatalf("filtered export = %+v", filtered)
	}
}

func TestAuditExportOutputFileIsPrivateAndNeverOverwritten(t *testing.T) {
	_, path := auditServer(t, 3, 0)
	file := filepath.Join(t.TempDir(), "audit.jsonl")

	got := runAuth(t, authRun{config: path}, "audit", "export", "--output", file)
	if got.code != 0 || !strings.Contains(got.stderr, "Head hash: h3") {
		t.Fatalf("export = %+v", got)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file = %v, %v", info, err)
	}
	if again := runAuth(t, authRun{config: path}, "audit", "export", "--output", file); again.code == 0 {
		t.Fatalf("an existing file must not be overwritten: %+v", again)
	}
	if data, _ := os.ReadFile(file); !strings.Contains(string(data), `"headHash":"h3"`) {
		t.Fatalf("original file was changed: %s", data)
	}
}

func TestAuditRefusesAnAPIToken(t *testing.T) {
	server := newAuthServer(t, false)
	server.seedAudit(2, 0)
	server.addToken("mch_pat_ci-0001", "ci", "operator")
	tokenPath := seed(t, server.URL, cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_ci-0001", User: "maxim", Name: "ci", Role: "operator", ExpiresAt: future()})

	for _, args := range [][]string{{"audit"}, {"audit", "verify"}, {"audit", "export"}} {
		if got := runAuth(t, authRun{config: tokenPath}, args...); got.code == 0 || strings.Contains(got.stdout, "h2") {
			t.Fatalf("%v with a token = %+v", args, got)
		}
	}
}
