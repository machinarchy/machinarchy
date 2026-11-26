package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestInventoryRequests(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	client := newClient(t, respond(rec, http.StatusOK, `{}`, nil))

	tests := []struct {
		name       string
		call       func() error
		wantMethod string
		wantURI    string
		wantBody   string
	}{
		{"create environment", func() error { _, err := client.CreateEnvironment(ctx, "prod", "main", false); return err },
			http.MethodPost, "/api/v1/environments", `{"description":"main","name":"prod"}`},
		{"delete environment", func() error { return client.DeleteEnvironment(ctx, 7) },
			http.MethodDelete, "/api/v1/environments/7", ""},
		{"get access profile", func() error { _, err := client.GetAccessProfile(ctx, "p 1"); return err },
			http.MethodGet, "/api/v1/managed/access-profiles/p%201", ""},
		{"list agents", func() error { _, err := client.ListAgents(ctx); return err },
			http.MethodGet, "/api/v1/agents", ""},
		{"revoke agent", func() error { return client.RevokeAgent(ctx, "a/b") },
			http.MethodPost, "/api/v1/agents/a%2Fb/revoke", ""},
		{"controller key", func() error { _, err := client.ControllerKey(ctx); return err },
			http.MethodGet, "/api/v1/agents/controller-key", ""},
		{"hosts phase and limit", func() error {
			_, err := client.ListHosts(ctx, HostQuery{Phase: "Pending", Limit: 50})
			return err
		}, http.MethodGet, "/api/v1/managed/hosts?limit=50&phase=Pending", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err != nil {
				t.Fatalf("call: %v", err)
			}
			got := rec.last(t)
			if got.Method != test.wantMethod || got.RequestURI != test.wantURI {
				t.Errorf("request = %s %s, want %s %s", got.Method, got.RequestURI, test.wantMethod, test.wantURI)
			}
			if string(got.Body) != test.wantBody {
				t.Errorf("body = %q, want %q", got.Body, test.wantBody)
			}
		})
	}
}

func TestInventoryDecoding(t *testing.T) {
	ctx := context.Background()

	t.Run("access profile detail carries references only", func(t *testing.T) {
		body := `{"id":"p1","name":"deploy","currentRevision":2,"spec":{"connectorKind":"ssh","username":"ops","credential":{"secretId":"s1"}}}`
		client := newClient(t, respond(&recorder{}, http.StatusOK, body, nil))
		profile, err := client.GetAccessProfile(ctx, "p1")
		if err != nil {
			t.Fatalf("GetAccessProfile: %v", err)
		}
		if profile.Name != "deploy" || profile.CurrentRevision != 2 || profile.Spec["username"] != "ops" {
			t.Fatalf("profile = %+v", profile)
		}
	})

	t.Run("controller key", func(t *testing.T) {
		body := `{"publicKey":"k1","publicKeys":["k1","k2"],"nextPublicKey":"k2"}`
		client := newClient(t, respond(&recorder{}, http.StatusOK, body, nil))
		key, err := client.ControllerKey(ctx)
		if err != nil {
			t.Fatalf("ControllerKey: %v", err)
		}
		if key.PublicKey != "k1" || len(key.PublicKeys) != 2 || key.NextPublicKey != "k2" {
			t.Fatalf("key = %+v", key)
		}
	})

	t.Run("agent list is passed through untouched", func(t *testing.T) {
		body := `{"items":[{"id":"a1"}]}`
		client := newClient(t, respond(&recorder{}, http.StatusOK, body, nil))
		raw, err := client.ListAgents(ctx)
		if err != nil || string(raw) != body {
			t.Fatalf("ListAgents = %q, %v", raw, err)
		}
	})
}

// The names the user confirmed travel with every guarded call: the run, a
// host move, trust and delete. Without any, no field is sent.
func TestConfirmedEnvironmentsAreSentOnGuardedCalls(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	confirmed := []string{"production", "staging"}
	client := newClient(t, respond(rec, http.StatusOK, `{}`, nil), WithConfirmedEnvs(func() []string { return confirmed }))

	_, _ = client.SubmitOperation(ctx, OperationInput{Targets: TargetRule{All: true}})
	if got := string(rec.last(t).Body); !strings.Contains(got, `"confirmEnvironment":["production","staging"]`) {
		t.Errorf("run body = %s", got)
	}
	_ = client.MoveHost(ctx, "h1", 1, 1, HostSpec{}, 2)
	if got := string(rec.last(t).Body); !strings.Contains(got, `"confirmEnvironment":["production","staging"]`) {
		t.Errorf("move body = %s", got)
	}
	_, _ = client.TrustHostKeys(ctx, "h1", 1, []string{"SHA256:x"}, false)
	if got := string(rec.last(t).Body); !strings.Contains(got, `"confirmEnvironment":["production","staging"]`) {
		t.Errorf("trust body = %s", got)
	}
	_ = client.DeleteHost(ctx, "h1", 4)
	if got := rec.last(t).RequestURI; got != "/api/v1/managed/hosts/h1?confirmEnvironment=production&confirmEnvironment=staging&revision=4" {
		t.Errorf("delete uri = %s", got)
	}
	confirmed = nil
	_, _ = client.SubmitOperation(ctx, OperationInput{Targets: TargetRule{All: true}})
	if got := string(rec.last(t).Body); strings.Contains(got, "confirmEnvironment") {
		t.Errorf("run body without confirmation = %s", got)
	}
	_, _ = client.CreateEnvironment(ctx, "prod", "", true)
	if got := string(rec.last(t).Body); !strings.Contains(got, `"protected":true`) {
		t.Errorf("create protected body = %s", got)
	}
}
