// Package clierr classifies CLI failures and maps them to stable process exit
// codes. It has no dependencies on the rest of the CLI so that every layer
// (configuration, API client, commands) can return classified errors.
package clierr

import (
	"context"
	"errors"
	"fmt"
)

// Exit codes returned by the machinarchy process. They follow the contract in
// ADR-035 and docs/architecture/phase-8-api-cli-interaction.md.
const (
	// ExitOK reports success.
	ExitOK = 0
	// ExitFailure reports an unsuccessful operation or an unexpected CLI error.
	ExitFailure = 1
	// ExitUsage reports invalid usage, arguments, local configuration, or input
	// that the API rejected as invalid.
	ExitUsage = 2
	// ExitAuth reports an authentication or authorization failure.
	ExitAuth = 3
	// ExitUnavailable reports an API server, network, or request-timeout failure.
	ExitUnavailable = 4
	// ExitNotFound reports that a requested resource does not exist.
	ExitNotFound = 6
	// ExitConflict reports a conflict or failed precondition.
	ExitConflict = 7
	// ExitInterrupted reports that the process was interrupted locally.
	ExitInterrupted = 130
)

// Error is an error with an exit code and an optional remediation hint.
type Error struct {
	Code int
	Err  error
	Hint string
}

// Error returns the underlying message.
func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}

	return e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}

// ExitCode returns the process exit code for this error.
func (e *Error) ExitCode() int {
	return e.Code
}

// ErrorHint returns remediation advice, or an empty string.
func (e *Error) ErrorHint() string {
	if e == nil {
		return ""
	}

	return e.Hint
}

// Wrap attaches an exit code and hint to err. It returns nil for a nil err.
// An error that already carries an exit code is returned unchanged so that the
// most specific classification wins.
func Wrap(code int, err error, hint string) error {
	if err == nil {
		return nil
	}

	if _, classified := Classify(err); classified {
		return err
	}

	return &Error{Code: code, Err: err, Hint: hint}
}

// Newf creates a classified error from a format string.
func Newf(code int, hint, format string, args ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...), Hint: hint}
}

// Usage classifies err as a usage or input error (exit code 2).
func Usage(err error) error {
	return Wrap(ExitUsage, err, "")
}

// Usagef creates a usage error (exit code 2) from a format string.
func Usagef(format string, args ...any) error {
	return Newf(ExitUsage, "", format, args...)
}

// Classify reports the exit code carried by err, if any. Local cancellation is
// always reported as ExitInterrupted.
func Classify(err error) (int, bool) {
	if err == nil {
		return ExitOK, true
	}

	if errors.Is(err, context.Canceled) {
		return ExitInterrupted, true
	}

	var coder interface{ ExitCode() int }
	if errors.As(err, &coder) {
		return coder.ExitCode(), true
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return ExitUnavailable, true
	}

	return ExitFailure, false
}

// ExitCode returns the process exit code for err. Unclassified errors map to
// ExitFailure.
func ExitCode(err error) int {
	code, _ := Classify(err)

	return code
}

// Hint returns remediation advice carried by err, or an empty string.
func Hint(err error) string {
	var hinter interface{ ErrorHint() string }
	if errors.As(err, &hinter) {
		return hinter.ErrorHint()
	}

	return ""
}
