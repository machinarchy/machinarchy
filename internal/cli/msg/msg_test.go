package msg

import (
	"regexp"
	"strings"
	"testing"
)

// loreAllowed is the complete list of messages that may carry a lore line
// (docs/cli/spec/07-brand.md section 2.1). Adding to it is a deliberate,
// reviewed act.
var loreAllowed = map[string]bool{
	"run.all_ok":         true,
	"auth.not_signed_in": true,
	"auth.signed_in":     true,
	"host.added":         true,
	"host.added_agent":   true,
	"host.trusted":       true,
	"help.hosts":         true,
	"help.run":           true,
}

var tokenShaped = regexp.MustCompile(`mch_pat_[A-Za-z0-9]{8,}|mae_[A-Za-z0-9]{8,}|eyJ[A-Za-z0-9_-]{10,}`)

// U-01: every error carries a next step.
func TestEveryErrorHasANextStep(t *testing.T) {
	t.Parallel()

	for _, m := range All() {
		if m.Kind == KindError && strings.TrimSpace(m.Next) == "" {
			t.Errorf("error message %q has no Next step", m.ID)
		}
	}
}

// U-04: lore is only on the allow-list, only on success messages, and never in
// errors, warnings, failures or security text.
func TestLoreOnlyWhereAllowed(t *testing.T) {
	t.Parallel()

	for _, m := range All() {
		if m.Lore == "" {
			continue
		}
		if !loreAllowed[m.ID] {
			t.Errorf("message %q has lore but is not on the allow-list", m.ID)
		}
		if m.Kind != KindSuccess && m.Kind != KindInfo {
			t.Errorf("message %q has lore but is a %s message", m.ID, m.Kind)
		}
	}
}

func TestNoTokenShapedText(t *testing.T) {
	t.Parallel()

	for _, m := range All() {
		for _, text := range []string{m.Primary, m.Why, m.Next, m.Lore} {
			if tokenShaped.MatchString(text) {
				t.Errorf("message %q contains token-shaped text", m.ID)
			}
		}
	}
}

func TestPrimaryIsPlainAndPresent(t *testing.T) {
	t.Parallel()

	for _, m := range All() {
		if strings.TrimSpace(m.Primary) == "" {
			t.Errorf("message %q has no primary line", m.ID)
		}
		if strings.Contains(m.Primary, "\n") {
			t.Errorf("message %q primary line spans several lines", m.ID)
		}
	}
}

// The words the spec forbids in user-facing text (07-brand.md section 2).
func TestNoForbiddenVocabulary(t *testing.T) {
	t.Parallel()

	forbidden := regexp.MustCompile(`(?i)\b(cluster|context|selector|connector|supervisor|access profile)\b`)
	for _, m := range All() {
		for _, text := range []string{m.Primary, m.Why, m.Next} {
			if loc := forbidden.FindString(text); loc != "" {
				t.Errorf("message %q uses the forbidden word %q", m.ID, loc)
			}
		}
	}
}

func TestWithReplacesPlaceholders(t *testing.T) {
	t.Parallel()

	got := Get("input.required").With(Vars{"flag": "--email", "why": "no terminal", "next": "machinarchy login --email x"})
	want := "--email is required when nothing can be asked."
	if got.Primary != want || got.Why != "no terminal" || got.Next != "machinarchy login --email x" {
		t.Fatalf("Primary = %q, want %q", got.Primary, want)
	}
}

func TestWithLeavesMissingPlaceholdersVisible(t *testing.T) {
	t.Parallel()

	got := Get("run.partial").With(Vars{"ok": "9", "total": "14"})
	if !strings.Contains(got.Primary, "{failed}") {
		t.Fatalf("a missing value must stay visible, got %q", got.Primary)
	}
}

func TestWithDoesNotMutateTheCatalogue(t *testing.T) {
	t.Parallel()

	_ = Get("run.partial").With(Vars{"ok": "1", "total": "2", "failed": "1", "id": "r-1"})
	if !strings.Contains(Get("run.partial").Primary, "{ok}") {
		t.Fatal("With changed the catalogue entry")
	}
}

func TestGetUnknownIsVisible(t *testing.T) {
	t.Parallel()

	got := Get("no.such.message")
	if got.Kind != KindError || !strings.Contains(got.Primary, "no.such.message") || got.Next == "" {
		t.Fatalf("unknown message placeholder = %+v", got)
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("registering a duplicate ID must panic")
		}
	}()
	register(Message{ID: "run.partial", Primary: "x"})
}

func TestAllIsSorted(t *testing.T) {
	t.Parallel()

	all := All()
	for i := 1; i < len(all); i++ {
		if all[i-1].ID >= all[i].ID {
			t.Fatalf("All() is not sorted at %q, %q", all[i-1].ID, all[i].ID)
		}
	}
}

func TestKindNames(t *testing.T) {
	t.Parallel()

	want := map[Kind]string{KindInfo: "info", KindSuccess: "success", KindWarning: "warning", KindFailure: "failure",
		KindError: "error", KindSecurity: "security", KindPrompt: "prompt", Kind(99): "unknown"}
	for kind, name := range want {
		if got := kind.String(); got != name {
			t.Errorf("Kind(%d).String() = %q, want %q", kind, got, name)
		}
	}
}

func TestWithNextAndWithWhyReplaceOnlyTheirField(t *testing.T) {
	t.Parallel()

	base := Get("run.partial")
	next := base.WithNext("machinarchy run r-1 --retry-failed")
	why := base.WithWhy("two hosts were unreachable")
	if next.Next != "machinarchy run r-1 --retry-failed" || next.Primary != base.Primary || next.Why != base.Why {
		t.Fatalf("WithNext changed more than Next: %+v", next)
	}
	if why.Why != "two hosts were unreachable" || why.Next != base.Next || why.Primary != base.Primary {
		t.Fatalf("WithWhy changed more than Why: %+v", why)
	}
	if base.Why != "" {
		t.Fatal("the catalogue entry was modified")
	}
}
