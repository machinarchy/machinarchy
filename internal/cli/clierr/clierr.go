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
// ADR-035 and docs/archive/phase-8-api-cli-interaction.md.
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
	// ExitWaitTimeout reports that a local wait deadline (--wait-timeout)
	// elapsed while the work it was watching is still running.
	ExitWaitTimeout = 5
	// ExitNotFound reports that a requested resource does not exist.
	ExitNotFound = 6
	// ExitConflict reports a conflict or failed precondition.
	ExitConflict = 7
	// ExitPartial reports a run that finished with some hosts failed.
	ExitPartial = 8
	// ExitAllFailed reports a run that finished with every host failed.
	ExitAllFailed = 9
	// ExitInterrupted reports that the process was interrupted locally.
	ExitInterrupted = 130
)

// Error is an error with an exit code, an optional explanation of the cause
// and an optional next step. Printed, they are the three parts of the unified
// format (docs/cli/spec/02-commands.md section 2.3):
//
//	Error: what happened     (Err)
//	Why:   the cause         (Why)
//	Next:  what to do        (Hint)
//
// Hint keeps its historical name: it is the remediation advice.
type Error struct {
	Code int
	Err  error
	// Why explains the cause, when it is known.
	Why string
	// Hint is the next step: the exact command to run, or advice.
	Hint string
	// Reported means the command already told the user everything on its own
	// (a grouped result, an interrupt notice); only the exit code is left.
	Reported bool
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

// ErrorWhy returns the explanation of the cause, or an empty string.
func (e *Error) ErrorWhy() string {
	if e == nil {
		return ""
	}

	return e.Why
}

// Reported returns an error that carries only an exit code: the command has
// already printed what the user needs, so nothing more is shown.
func Reported(code int) error {
	return &Error{Code: code, Err: fmt.Errorf("exit %d", code), Reported: true}
}

// IsReported reports whether err came from Reported.
func IsReported(err error) bool {
	var coded *Error

	return errors.As(err, &coded) && coded.Reported
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

// Detailed attaches an exit code, a cause and a next step to err. Like Wrap,
// it leaves an error that is already classified unchanged.
func Detailed(code int, err error, why, next string) error {
	if err == nil {
		return nil
	}

	if _, classified := Classify(err); classified {
		return err
	}

	return &Error{Code: code, Err: err, Why: why, Hint: next}
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

// Why returns the explanation of the cause carried by err, or an empty string.
func Why(err error) string {
	var whyer interface{ ErrorWhy() string }
	if errors.As(err, &whyer) {
		return whyer.ErrorWhy()
	}

	return ""
}

// Name returns the stable, lower-case name of an exit code, used in the
// machine-readable error of -o json.
func Name(code int) string {
	switch code {
	case ExitOK:
		return "ok"
	case ExitUsage:
		return "usage"
	case ExitAuth:
		return "auth"
	case ExitUnavailable:
		return "unavailable"
	case ExitWaitTimeout:
		return "waitTimeout"
	case ExitNotFound:
		return "notFound"
	case ExitConflict:
		return "conflict"
	case ExitPartial:
		return "partial"
	case ExitAllFailed:
		return "allFailed"
	case ExitInterrupted:
		return "interrupted"
	default:
		return "failure"
	}
}

// Hint returns remediation advice carried by err, or an empty string.
func Hint(err error) string {
	var hinter interface{ ErrorHint() string }
	if errors.As(err, &hinter) {
		return hinter.ErrorHint()
	}

	return ""
}
