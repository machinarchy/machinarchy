package commands_test

import (
	"net/http"
	"strings"
	"testing"
)

// C-03: a server that reports role, token name and expiry in /api/v1/me is
// believed over what the CLI saved; no secret is printed.
func TestWhoamiShowsWhatTheServerReports(t *testing.T) {
	server := newAPIServer(t, jsonReply(http.StatusOK,
		`{"id":1,"username":"maxim","role":"operator","authMethod":"api_token","tokenId":7,"tokenName":"ci-deploy","expiresAt":"2031-05-06T07:08:00Z"}`))
	config := writeConfig(t, server.URL, "mch_pat_secret-0001")

	got := execute(t, nil, "", "--config", config, "whoami")
	for _, want := range []string{"Role:           operator", "ci-deploy", "2031-05-06"} {
		if got.code != 0 || !strings.Contains(got.stdout, want) {
			t.Fatalf("whoami lacks %q: %+v", want, got)
		}
	}
	if strings.Contains(got.stdout+got.stderr, "mch_pat_secret-0001") || strings.Contains(got.stdout, "not reported") {
		t.Fatalf("whoami = %+v", got)
	}
	if json := execute(t, nil, "", "--config", config, "whoami", "-o", "json"); !strings.Contains(json.stdout, `"role": "operator"`) ||
		!strings.Contains(json.stdout, `"expiresAt": "2031-05-06T07:08:00Z"`) {
		t.Fatalf("whoami -o json = %+v", json)
	}
}

// An older server reports no role: the CLI says so and never guesses.
func TestWhoamiOnAnOlderServerSaysNotReported(t *testing.T) {
	server := newAPIServer(t, jsonReply(http.StatusOK, `{"id":1,"username":"maxim"}`))
	config := writeConfig(t, server.URL, "tok")
	if got := execute(t, nil, "", "--config", config, "whoami"); got.code != 0 || !strings.Contains(got.stdout, "not reported by this server") {
		t.Fatalf("whoami = %+v", got)
	}
}
