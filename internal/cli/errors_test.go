package cli_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

func failing(name string, err error) cli.CommandBuilder {
	return command(name, func(*cobra.Command, *cli.Factory) error { return err })
}

// The unified format has three parts: what, why, next.
func TestErrorHasWhatWhyAndNext(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")
	options := testOptions(failing("denied", clierr.Detailed(clierr.ExitAuth,
		errors.New("could not sign in to machinarchy.example (HTTP 401)"),
		"the saved session expired at 09:14", "machinarchy login")))

	got := runWithEnv(t, nil, options, "--config", none, "denied")
	want := "Error: could not sign in to machinarchy.example (HTTP 401)\n" +
		"Why:   the saved session expired at 09:14\n" +
		"Next:  machinarchy login\n"
	if got.code != clierr.ExitAuth || got.stdout != "" || got.stderr != want {
		t.Fatalf("result = %+v\nwant stderr:\n%s", got, want)
	}
}

func TestAPIErrorsShowWhyAndNext(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")
	apiErr := &api.Error{Status: 403, Message: "permission denied", Method: "POST", Path: "/api/v1/managed/hosts/1/trust"}
	options := testOptions(failing("trust", fmt.Errorf("trust host: %w", apiErr)))

	got := runWithEnv(t, nil, options, "--config", none, "trust")
	for _, want := range []string{
		"Error: trust host: API returned HTTP 403: permission denied\n",
		"Why:   The credential is valid but is not permitted to do this.\n",
		"Next:  Commands that manage runtime configuration or secrets need a human owner session",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, got.stderr)
		}
	}
	if got.code != clierr.ExitAuth {
		t.Fatalf("exit = %d", got.code)
	}
}

// N-03: every documented exit code can be produced and has its own name.
func TestEveryExitCode(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")

	codes := map[string]int{
		"c1": clierr.ExitFailure, "c2": clierr.ExitUsage, "c3": clierr.ExitAuth, "c4": clierr.ExitUnavailable,
		"c5": clierr.ExitWaitTimeout, "c6": clierr.ExitNotFound, "c7": clierr.ExitConflict,
		"c8": clierr.ExitPartial, "c9": clierr.ExitAllFailed,
	}
	var builders []cli.CommandBuilder
	for name, code := range codes {
		builders = append(builders, failing(name, clierr.Newf(code, "retry", "failed with %d", code)))
	}
	options := testOptions(builders...)

	seen := map[string]bool{}
	for name, code := range codes {
		got := runWithEnv(t, nil, options, "--config", none, name)
		if got.code != code {
			t.Errorf("%s exited %d, want %d", name, got.code, code)
		}
		if !strings.HasPrefix(got.stderr, fmt.Sprintf("Error: failed with %d\n", code)) {
			t.Errorf("%s printed %q", name, got.stderr)
		}
		seen[clierr.Name(code)] = true
	}
	if len(seen) != len(codes) {
		t.Fatalf("exit codes share names: %v", seen)
	}
}

type jsonErr struct {
	Error struct {
		Code    string `json:"code"`
		Exit    int    `json:"exit"`
		Message string `json:"message"`
		Why     string `json:"why"`
		Next    string `json:"next"`
	} `json:"error"`
}

// With -o json the error is one JSON object on stderr and stdout stays empty.
func TestJSONErrors(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")
	options := testOptions(failing("denied", clierr.Detailed(clierr.ExitAuth, errors.New("no access"), "role too low", "machinarchy login")))

	for name, tc := range map[string]struct {
		env  map[string]string
		args []string
	}{
		"flag":        {nil, []string{"--config", none, "-o", "json", "denied"}},
		"environment": {map[string]string{"MACHINARCHY_OUTPUT": "json"}, []string{"--config", none, "denied"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := runWithEnv(t, tc.env, options, tc.args...)
			if got.code != clierr.ExitAuth || got.stdout != "" {
				t.Fatalf("result = %+v", got)
			}

			var decoded jsonErr
			if err := json.Unmarshal([]byte(got.stderr), &decoded); err != nil {
				t.Fatalf("stderr is not JSON: %v\n%s", err, got.stderr)
			}
			e := decoded.Error
			if e.Code != "auth" || e.Exit != 3 || e.Message != "no access" || e.Why != "role too low" || e.Next != "machinarchy login" {
				t.Fatalf("error = %+v", e)
			}
			if strings.Count(got.stderr, "\n") != 1 {
				t.Fatalf("the JSON error must be one line: %q", got.stderr)
			}
		})
	}
}

// The error that says the config is broken still honours -o json and --plain,
// because the flags and the environment are enough to know how to print it.
func TestErrorPresentationDoesNotNeedTheConfig(t *testing.T) {
	broken := writeBrokenConfig(t)

	got := runWithEnv(t, nil, testOptions(), "--config", broken, "-o", "json", "ok")
	var decoded jsonErr
	if got.code != clierr.ExitUsage || json.Unmarshal([]byte(got.stderr), &decoded) != nil || decoded.Error.Code != "usage" {
		t.Fatalf("a broken config must still give a JSON error with -o json: %+v", got)
	}

	got = runWithEnv(t, map[string]string{"MACHINARCHY_OUTPUT": "json"}, testOptions(), "--config", broken, "ok")
	if json.Unmarshal([]byte(got.stderr), &decoded) != nil {
		t.Fatalf("the environment must decide too: %+v", got)
	}

	got = runWithEnv(t, nil, testOptions(), "--config", broken, "--plain", "ok")
	if got.code != clierr.ExitUsage || !strings.HasPrefix(got.stderr, "Error: ") {
		t.Fatalf("result = %+v", got)
	}
}

// S-13 at the CLI level: a hostile value inside an error cannot drive the
// terminal.
func TestErrorTextIsSanitised(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")
	hostile := "web-01\x1b[2J\x1b]52;c;ZXZpbA==\x07"
	options := testOptions(failing("bad", clierr.Detailed(clierr.ExitNotFound,
		fmt.Errorf("host %q was not found", hostile), "name "+hostile, "machinarchy hosts show "+hostile)))

	got := runWithEnv(t, nil, options, "--config", none, "bad")
	if strings.ContainsRune(got.stderr, 0x1b) || strings.ContainsRune(got.stderr, 0x07) {
		t.Fatalf("an escape sequence reached stderr: %q", got.stderr)
	}
	if got.code != clierr.ExitNotFound {
		t.Fatalf("exit = %d", got.code)
	}
}

func TestInterruptedIsNotAnError(t *testing.T) {
	none := filepath.Join(t.TempDir(), "absent.yaml")

	got := runWithEnv(t, nil, testOptions(failing("stop", fmt.Errorf("wait: %w", errors.New("x")))), "--config", none, "fail-canceled")
	if got.code != clierr.ExitInterrupted || got.stderr != "Interrupted.\n" {
		t.Fatalf("result = %+v", got)
	}
}
