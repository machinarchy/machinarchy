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

func (s *authServer) auditRoute(r *http.Request, raw []byte, reply func(int, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/api/v1/audit/prune" && r.Method == http.MethodPost {
		s.prunes = append(s.prunes, string(raw))
		var req struct {
			BeforeID        int64  `json:"beforeId"`
			ArchiveHeadHash string `json:"archiveHeadHash"`
			ArchiveCount    int    `json:"archiveCount"`
		}
		_ = json.Unmarshal(raw, &req)
		var keep []map[string]any
		count, lastHash, lastID := 0, "", int64(0)
		for _, e := range s.audit {
			if id := e["id"].(int64); id < req.BeforeID {
				count++
				lastHash, lastID = e["hash"].(string), id
			} else {
				keep = append(keep, e)
			}
		}
		if count != req.ArchiveCount || lastHash != req.ArchiveHeadHash {
			reply(409, `{"code":"conflict","message":"the archive does not match the events to remove"}`)

			return
		}
		s.audit = keep
		body, _ := json.Marshal(map[string]any{"id": 1, "lastEventId": lastID, "lastHash": lastHash, "eventsRemoved": count, "createdAt": "2026-11-27T10:00:00Z"})
		reply(200, string(body))

		return
	}
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

	for _, args := range [][]string{{"audit"}, {"audit", "verify"}, {"audit", "export"}, {"audit", "prune", "--before-id", "2", "--archive", "x", "--confirm", "2"}} {
		if got := runAuth(t, authRun{config: tokenPath}, args...); got.code == 0 || strings.Contains(got.stdout, "h2") {
			t.Fatalf("%v with a token = %+v", args, got)
		}
	}
}

func (s *authServer) prunesSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.prunes...)
}

func exportTo(t *testing.T, path string, args ...string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "audit.jsonl")
	got := runAuth(t, authRun{config: path}, append([]string{"audit", "export", "--output", file}, args...)...)
	if got.code != 0 && !strings.Contains(strings.Join(args, " "), "--action") && !strings.Contains(got.stderr, "broken") {
		t.Fatalf("export = %+v", got)
	}

	return file
}

func TestAuditPruneSendsOnlyTheProofAndRemovesTheRange(t *testing.T) {
	server, path := auditServer(t, 10, 0)
	archive := exportTo(t, path)

	got := runAuth(t, authRun{config: path}, "audit", "prune", "--before-id", "6", "--archive", archive, "--confirm", "6")
	if got.code != 0 || !strings.Contains(got.stdout, "Removed 5 events up to event 5") || !strings.Contains(got.stdout, "Checkpoint hash: h5") {
		t.Fatalf("prune = %+v", got)
	}
	sent := server.prunesSeen()
	if len(sent) != 1 || sent[0] != `{"beforeId":6,"archiveHeadHash":"h5","archiveCount":5}` {
		t.Fatalf("request = %v; want only the cutoff, head hash and count", sent)
	}
	if left := runAuth(t, authRun{config: path}, "audit", "--limit", "50"); strings.Contains(left.stdout, "member.role_changed 5") || !strings.Contains(left.stdout, "10") {
		t.Fatalf("list after prune = %+v", left)
	}
}

func TestAuditPruneNeedsTypedConfirmationAndHonoursDryRun(t *testing.T) {
	server, path := auditServer(t, 6, 0)
	archive := exportTo(t, path)

	if got := runAuth(t, authRun{config: path}, "audit", "prune", "--before-id", "4", "--archive", archive, "--yes"); got.code != clierr.ExitUsage {
		t.Fatalf("--yes must not confirm a prune: %+v", got)
	}
	if got := runAuth(t, authRun{config: path}, "audit", "prune", "--before-id", "4", "--archive", archive, "--confirm", "5"); got.code != clierr.ExitUsage {
		t.Fatalf("a wrong --confirm must be refused: %+v", got)
	}
	dry := runAuth(t, authRun{config: path}, "audit", "prune", "--before-id", "4", "--archive", archive, "--dry-run")
	if dry.code != 0 || !strings.Contains(dry.stdout, "Would remove 3 events") {
		t.Fatalf("dry run = %+v", dry)
	}
	if sent := server.prunesSeen(); len(sent) != 0 {
		t.Fatalf("nothing may be sent without confirmation: %v", sent)
	}
}

func TestAuditPruneRefusesUnusableArchivesBeforeAnyRequest(t *testing.T) {
	server, path := auditServer(t, 6, 0)
	dir := t.TempDir()
	write := func(name, content string) string {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		return file
	}
	full := exportTo(t, path)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	noSummary := write("nosummary.jsonl", strings.Join(lines[:len(lines)-1], "\n")+"\n")
	filtered := exportTo(t, path, "--action", "host.created")
	notEvents := write("junk.jsonl", "hello\n"+lines[len(lines)-1]+"\n")
	duplicate := write("dup.jsonl", lines[0]+"\n"+lines[0]+"\n"+lines[len(lines)-1]+"\n")
	gap := write("gap.jsonl", lines[0]+"\n"+lines[len(lines)-2]+"\n"+lines[len(lines)-1]+"\n") // events 6 and 1 do not link

	for name, archive := range map[string]string{
		"missing summary": noSummary, "filtered export": filtered, "not events": notEvents,
		"duplicate event": duplicate, "unlinked events": gap, "missing file": filepath.Join(dir, "none.jsonl"),
	} {
		got := runAuth(t, authRun{config: path}, "audit", "prune", "--before-id", "7", "--archive", archive, "--confirm", "7")
		if got.code != clierr.ExitUsage {
			t.Errorf("%s: exit = %d (%s), want a usage error", name, got.code, got.stderr)
		}
	}
	for name, args := range map[string][]string{
		"no archive": {"--before-id", "6"}, "no cutoff": {"--archive", full}, "first event": {"--before-id", "1", "--archive", full},
	} {
		got := runAuth(t, authRun{config: path}, append([]string{"audit", "prune", "--confirm", "6"}, args...)...)
		if got.code != clierr.ExitUsage {
			t.Errorf("%s: exit = %d, want a usage error", name, got.code)
		}
	}
	if sent := server.prunesSeen(); len(sent) != 0 {
		t.Fatalf("unusable archives must never reach the server: %v", sent)
	}
}

func TestAuditPruneArchiveOfABrokenChainIsRefused(t *testing.T) {
	server, path := auditServer(t, 6, 3)
	archive := exportTo(t, path)
	if got := runAuth(t, authRun{config: path}, "audit", "prune", "--before-id", "5", "--archive", archive, "--confirm", "5"); got.code != clierr.ExitUsage {
		t.Fatalf("an archive of a broken chain must be refused: %+v", got)
	}
	if sent := server.prunesSeen(); len(sent) != 0 {
		t.Fatalf("request sent: %v", sent)
	}
}

func TestAuditPruneServerRefusesAPartialArchive(t *testing.T) {
	_, path := auditServer(t, 10, 0)
	partial := exportTo(t, path, "--max", "3") // events 10, 9, 8 only
	got := runAuth(t, authRun{config: path}, "audit", "prune", "--before-id", "10", "--archive", partial, "--confirm", "10")
	if got.code == 0 || !strings.Contains(got.stderr, "archive") {
		t.Fatalf("a partial archive must be refused by the server: %+v", got)
	}
	if list := runAuth(t, authRun{config: path}, "audit", "--limit", "50"); !strings.Contains(list.stdout, "member.role_changed") {
		t.Fatalf("events were removed despite the refusal: %+v", list)
	}
}
