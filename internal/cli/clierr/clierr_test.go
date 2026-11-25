package clierr

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"unclassified", errors.New("boom"), ExitFailure},
		{"usage", Usagef("bad flag"), ExitUsage},
		{"explicit code", Newf(ExitNotFound, "", "missing"), ExitNotFound},
		{"wrapped classified", fmt.Errorf("outer: %w", Newf(ExitConflict, "", "stale")), ExitConflict},
		{"canceled", context.Canceled, ExitInterrupted},
		{"wrapped canceled", fmt.Errorf("request: %w", context.Canceled), ExitInterrupted},
		{"deadline", context.DeadlineExceeded, ExitUnavailable},
		{"canceled beats code", &Error{Code: ExitUnavailable, Err: context.Canceled}, ExitInterrupted},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := ExitCode(test.err); got != test.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", test.err, got, test.want)
			}
		})
	}
}

func TestWrapKeepsExistingClassification(t *testing.T) {
	t.Parallel()

	inner := Newf(ExitAuth, "log in again", "unauthorized")

	wrapped := Wrap(ExitUsage, inner, "other hint")
	if got := ExitCode(wrapped); got != ExitAuth {
		t.Fatalf("exit code = %d, want %d", got, ExitAuth)
	}

	if got := Hint(wrapped); got != "log in again" {
		t.Fatalf("hint = %q, want original hint", got)
	}
}

func TestWrapNil(t *testing.T) {
	t.Parallel()

	if err := Wrap(ExitUsage, nil, ""); err != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", err)
	}

	if err := Usage(nil); err != nil {
		t.Fatalf("Usage(nil) = %v, want nil", err)
	}
}

func TestHintAndMessage(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("context: %w", Newf(ExitUsage, "try --help", "bad input %d", 7))
	if got := err.Error(); got != "context: bad input 7" {
		t.Fatalf("message = %q", got)
	}

	if got := Hint(err); got != "try --help" {
		t.Fatalf("hint = %q", got)
	}

	if got := Hint(errors.New("plain")); got != "" {
		t.Fatalf("hint for plain error = %q, want empty", got)
	}
}

func TestClassifyReportsUnclassified(t *testing.T) {
	t.Parallel()

	if code, ok := Classify(errors.New("plain")); ok || code != ExitFailure {
		t.Fatalf("Classify(plain) = (%d, %v), want (%d, false)", code, ok, ExitFailure)
	}
}

func TestNewExitCodes(t *testing.T) {
	t.Parallel()

	want := map[int]string{
		ExitOK: "ok", ExitFailure: "failure", ExitUsage: "usage", ExitAuth: "auth", ExitUnavailable: "unavailable",
		ExitWaitTimeout: "waitTimeout", ExitNotFound: "notFound", ExitConflict: "conflict",
		ExitPartial: "partial", ExitAllFailed: "allFailed", ExitInterrupted: "interrupted",
	}
	if ExitWaitTimeout != 5 || ExitPartial != 8 || ExitAllFailed != 9 {
		t.Fatalf("codes = %d, %d, %d; want 5, 8, 9", ExitWaitTimeout, ExitPartial, ExitAllFailed)
	}
	for code, name := range want {
		if got := Name(code); got != name {
			t.Errorf("Name(%d) = %q, want %q", code, got, name)
		}
	}
}

func TestDetailedKeepsWhyAndNext(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("wrapped: %w", Detailed(ExitAuth, errors.New("denied"), "the session expired", "machinarchy login"))
	if Why(err) != "the session expired" || Hint(err) != "machinarchy login" {
		t.Fatalf("why = %q, next = %q", Why(err), Hint(err))
	}
	if code, ok := Classify(err); !ok || code != ExitAuth {
		t.Fatalf("Classify = %d, %v", code, ok)
	}
	if Why(errors.New("plain")) != "" {
		t.Fatal("a plain error has no why")
	}
	if Detailed(ExitAuth, nil, "x", "y") != nil {
		t.Fatal("Detailed(nil) must be nil")
	}
}

func TestDetailedKeepsTheMostSpecificClassification(t *testing.T) {
	t.Parallel()

	inner := Newf(ExitConflict, "retry", "stale")
	if got := Detailed(ExitUsage, inner, "why", "next"); got != inner {
		t.Fatal("an already classified error must be returned unchanged")
	}
}
