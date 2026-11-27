package commands_test

import (
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

func memberServer(t *testing.T) (*authServer, string) {
	t.Helper()
	server := newAuthServer(t, false)
	server.seedMembers(
		fakeMember{ID: 1, Username: "maxim", Role: "owner"},
		fakeMember{ID: 2, Username: "alice", Role: "operator"},
		fakeMember{ID: 3, Username: "bob", Role: "reader"},
	)

	return server, ownerConfig(t, server)
}

func TestMembersList(t *testing.T) {
	_, path := memberServer(t)

	got := runAuth(t, authRun{config: path}, "members")
	if got.code != 0 || !strings.Contains(got.stdout, "alice") || !strings.Contains(got.stdout, "operator") || !strings.Contains(got.stdout, "owner") {
		t.Fatalf("result = %+v", got)
	}
	if json := runAuth(t, authRun{config: path}, "members", "-o", "json"); json.code != 0 || !strings.Contains(json.stdout, `"username": "alice"`) {
		t.Fatalf("json result = %+v", json)
	}
}

func TestMembersRoleChange(t *testing.T) {
	server, path := memberServer(t)

	got := runAuth(t, authRun{config: path}, "members", "role", "alice", "owner")
	if got.code != 0 || !strings.Contains(got.stdout, "alice is now owner.") {
		t.Fatalf("result = %+v", got)
	}
	if server.memberList()[1].Role != "owner" {
		t.Fatalf("members = %+v", server.memberList())
	}
	if bad := runAuth(t, authRun{config: path}, "members", "role", "alice", "god"); bad.code != clierr.ExitUsage {
		t.Fatalf("a bad role must be a usage error: %+v", bad)
	}
	if unknown := runAuth(t, authRun{config: path}, "members", "role", "nobody", "reader"); unknown.code != clierr.ExitNotFound {
		t.Fatalf("an unknown member must be not found: %+v", unknown)
	}
	// The last owner is protected by the server; the CLI reports it.
	server.seedMembers(fakeMember{ID: 1, Username: "maxim", Role: "owner"})
	if last := runAuth(t, authRun{config: path}, "members", "role", "maxim", "reader"); last.code == 0 || !strings.Contains(last.stderr, "at least one owner") {
		t.Fatalf("demoting the last owner = %+v", last)
	}
}

func TestMembersRemove(t *testing.T) {
	server, path := memberServer(t)

	t.Run("--yes is not enough", func(t *testing.T) {
		got := runAuth(t, authRun{config: path}, "members", "remove", "alice", "--yes")
		if got.code != clierr.ExitUsage || len(server.memberList()) != 3 || len(server.saw("DELETE", "/api/v1/members/2")) != 0 {
			t.Fatalf("result = %+v, members = %+v", got, server.memberList())
		}
		if !strings.Contains(got.stderr, "--confirm alice") {
			t.Errorf("the error must name --confirm: %q", got.stderr)
		}
	})
	t.Run("a wrong name aborts and nothing is sent", func(t *testing.T) {
		got := runAuth(t, authRun{config: path}, "members", "remove", "alice", "--confirm", "bob")
		if got.code != clierr.ExitUsage || len(server.memberList()) != 3 {
			t.Fatalf("result = %+v", got)
		}
		wrong := runAuth(t, authRun{config: path, terminal: true, stdin: "nope\n"}, "members", "remove", "alice")
		if wrong.code == 0 || len(server.memberList()) != 3 {
			t.Fatalf("a wrong typed name must abort: %+v", wrong)
		}
	})
	t.Run("the typed name removes the member", func(t *testing.T) {
		got := runAuth(t, authRun{config: path, terminal: true, stdin: "alice\n"}, "members", "remove", "alice")
		if got.code != 0 || !strings.Contains(got.stdout, "alice no longer has access.") || len(server.memberList()) != 2 {
			t.Fatalf("result = %+v, members = %+v", got, server.memberList())
		}
		if calls := server.saw("DELETE", "/api/v1/members/2"); len(calls) != 1 || calls[0].Auth != "Bearer sess-owner" {
			t.Fatalf("calls = %+v", calls)
		}
	})
	t.Run("--confirm works without a terminal; ID also finds the member", func(t *testing.T) {
		got := runAuth(t, authRun{config: path}, "members", "remove", "3", "--confirm", "bob")
		if got.code != 0 || len(server.memberList()) != 1 {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("the last owner cannot be removed, even by themselves", func(t *testing.T) {
		got := runAuth(t, authRun{config: path}, "members", "remove", "maxim", "--confirm", "maxim")
		if got.code == 0 || !strings.Contains(got.stderr, "at least one owner") || len(server.memberList()) != 1 {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("an owner may remove themselves while another owner remains", func(t *testing.T) {
		server.seedMembers(fakeMember{ID: 1, Username: "maxim", Role: "owner"}, fakeMember{ID: 2, Username: "alice", Role: "owner"})
		got := runAuth(t, authRun{config: path}, "members", "remove", "maxim", "--confirm", "maxim")
		if got.code != 0 || len(server.memberList()) != 1 {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("an API token is refused locally before anything is sent", func(t *testing.T) {
		server.seedMembers(fakeMember{ID: 1, Username: "maxim", Role: "owner"}, fakeMember{ID: 2, Username: "alice", Role: "reader"})
		server.addToken("mch_pat_ci-0001", "ci", "operator")
		tokenPath := seed(t, server.URL, cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_ci-0001", User: "maxim", Name: "ci", Role: "operator", ExpiresAt: future()})
		before := len(server.saw("DELETE", "/api/v1/members/2"))
		got := runAuth(t, authRun{config: tokenPath}, "members", "remove", "alice", "--confirm", "alice")
		if got.code == 0 || len(server.saw("DELETE", "/api/v1/members/2")) != before || len(server.memberList()) != 2 {
			t.Fatalf("result = %+v (delete calls before: %d)", got, before)
		}
	})
}

func TestMembersInviteListRevoke(t *testing.T) {
	server, path := memberServer(t)

	got := runAuth(t, authRun{config: path}, "members", "invite", "new.person@example.test", "--role", "operator")
	if got.code != 0 || !strings.Contains(got.stdout, "Invitation sent to new.person@example.test as operator") {
		t.Fatalf("invite = %+v", got)
	}
	if calls := server.saw("POST", "/api/v1/members/invitations"); len(calls) != 1 || calls[0].Auth != "Bearer sess-owner" {
		t.Fatalf("calls = %+v", calls)
	}
	if invites := server.inviteList(); len(invites) != 1 || invites[0].Role != "operator" {
		t.Fatalf("invites = %+v", invites)
	}
	// The default role is the least one.
	if got := runAuth(t, authRun{config: path}, "members", "invite", "b@example.test"); got.code != 0 || server.inviteList()[1].Role != "reader" {
		t.Fatalf("default role: %+v, %+v", got, server.inviteList())
	}

	list := runAuth(t, authRun{config: path}, "members", "invites")
	if list.code != 0 || !strings.Contains(list.stdout, "new.person@example.test") || strings.Contains(strings.ToLower(list.stdout), "token") {
		t.Fatalf("invites = %+v", list)
	}
	if json := runAuth(t, authRun{config: path}, "members", "invites", "-o", "json"); json.code != 0 || !strings.Contains(json.stdout, `"email": "b@example.test"`) {
		t.Fatalf("json = %+v", json)
	}

	if got := runAuth(t, authRun{config: path}, "members", "revoke", "1"); got.code != 0 || !strings.Contains(got.stdout, "Invitation 1 revoked.") || len(server.inviteList()) != 1 {
		t.Fatalf("revoke = %+v", got)
	}
	if got := runAuth(t, authRun{config: path}, "members", "revoke", "99"); got.code == 0 {
		t.Fatalf("revoking an unknown invitation must fail: %+v", got)
	}
	if got := runAuth(t, authRun{config: path}, "members", "revoke", "abc"); got.code != clierr.ExitUsage {
		t.Fatalf("a bad id must be a usage error: %+v", got)
	}
}

func TestMembersInviteRefusals(t *testing.T) {
	server, path := memberServer(t)

	if got := runAuth(t, authRun{config: path}, "members", "invite", "a@example.test", "--role", "owner"); got.code != clierr.ExitUsage || len(server.inviteList()) != 0 || len(server.saw("POST", "/api/v1/members/invitations")) != 0 {
		t.Fatalf("an owner invitation must be refused locally: %+v", got)
	}
	server.noMail = true
	if got := runAuth(t, authRun{config: path}, "members", "invite", "a@example.test"); got.code == 0 || !strings.Contains(got.stderr, "not configured") {
		t.Fatalf("a server without mail must say so: %+v", got)
	}

	server.addToken("mch_pat_ci-0001", "ci", "operator")
	tokenPath := seed(t, server.URL, cred.Credential{Kind: cred.KindDeviceToken, Secret: "mch_pat_ci-0001", User: "maxim", Name: "ci", Role: "operator", ExpiresAt: future()})
	server.noMail = false
	before := len(server.saw("POST", "/api/v1/members/invitations"))
	if got := runAuth(t, authRun{config: tokenPath}, "members", "invite", "a@example.test"); got.code == 0 || len(server.saw("POST", "/api/v1/members/invitations")) != before {
		t.Fatalf("an API token must be refused locally before anything is sent: %+v", got)
	}
}
