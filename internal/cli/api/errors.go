package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

const (
	maxErrorBodyBytes = 64 << 10
	maxMessageRunes   = 500
	maxErrorFields    = 16
	maxRetryAfter     = 24 * time.Hour
)

// Error is a non-2xx response from the Machinarchy API. It carries the HTTP
// status so the CLI can pick an exit code and a remediation hint without
// parsing message text.
type Error struct {
	// Status is the HTTP status code.
	Status int
	// Message is the server's explanation, sanitised for terminal output.
	Message string
	// RequestID is the server's request correlation ID, when it sent one.
	RequestID string
	// RetryAfter is the server's requested pause for 429/503 responses.
	RetryAfter time.Duration
	// Location is the redirect target for 3xx responses.
	Location string
	// Fields holds the top-level string fields of the JSON error body, for
	// callers that need structured details such as a host-key fingerprint.
	Fields map[string]string
	// Method and Path identify the failed request. Query strings are omitted.
	Method string
	Path   string
}

// Error returns a one-line description. It never includes the credential.
func (e *Error) Error() string {
	message := fmt.Sprintf("API returned HTTP %d: %s", e.Status, e.Message)
	if e.RequestID != "" {
		message += " (request ID " + e.RequestID + ")"
	}

	return message
}

// ExitCode maps the HTTP status to a CLI exit code.
func (e *Error) ExitCode() int {
	switch {
	case e.Status == http.StatusUnauthorized, e.Status == http.StatusForbidden:
		return clierr.ExitAuth
	case e.Status == http.StatusNotFound:
		return clierr.ExitNotFound
	case e.Status == http.StatusConflict, e.Status == http.StatusPreconditionFailed:
		return clierr.ExitConflict
	case e.Status == http.StatusRequestTimeout, e.Status == http.StatusTooManyRequests:
		return clierr.ExitUnavailable
	case e.Status >= 300 && e.Status < 500:
		return clierr.ExitUsage
	case e.Status >= 500:
		return clierr.ExitUnavailable
	default:
		return clierr.ExitFailure
	}
}

// ErrorHint suggests how the user can resolve the failure.
func (e *Error) ErrorHint() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		return "The server did not accept the stored credential; it may be expired or revoked.\n" +
			"Store a new token with: printf '%s' \"$TOKEN\" | machinarchy config set-credentials NAME --token-stdin"
	case e.Status == http.StatusForbidden && e.Fields["code"] == "execution_not_allowed":
		return "The server's execution policy does not allow this command. An owner can add a matching entry,\n" +
			"such as \"systemctl is-active {unit}\", to operations.allowedPrograms with PUT /api/v1/runtime-config."
	case e.Status == http.StatusForbidden:
		return "The credential is valid but is not permitted to do this.\n" +
			"Commands that manage runtime configuration or secrets need a human owner session; API tokens (mch_pat_...) are refused for them."
	case e.Status == http.StatusNotFound:
		return "Check the identifier. If the whole feature is missing, the server may not have it enabled, or --server may point at the wrong address."
	case e.Status == http.StatusConflict && e.Fields["code"] == "already_exists":
		return "A resource with that name already exists. Choose another name, or look at it with 'machinarchy get'."
	case e.Status == http.StatusConflict, e.Status == http.StatusPreconditionFailed:
		return "The resource changed or a precondition failed. Read the current state and retry with an up-to-date revision."
	case e.Status == http.StatusTooManyRequests, e.Status == http.StatusServiceUnavailable:
		if e.RetryAfter > 0 {
			return fmt.Sprintf("The server asked for a pause. Retry in %s.", e.RetryAfter)
		}

		return "The server is rate limiting or temporarily unavailable. Wait a moment and retry."
	case e.Status >= 500:
		hint := "The server reported an internal error. Retry later."
		if e.RequestID != "" {
			hint += " Include the request ID when reporting the problem."
		}

		return hint
	case e.Status >= 300 && e.Status < 400:
		if e.Location != "" {
			return fmt.Sprintf("The server redirected the request to %q and the CLI does not follow redirects. Check that the server URL is correct (for example, use https://).", e.Location)
		}

		return "The server redirected the request and the CLI does not follow redirects. Check that the server URL is correct."
	default:
		return ""
	}
}

// Field returns a top-level string field of the JSON error body.
func (e *Error) Field(name string) string {
	return e.Fields[name]
}

// newError builds an Error from a non-2xx response and its (possibly
// truncated) body.
func newError(response *http.Response, method, path string, body []byte) *Error {
	apiErr := &Error{
		Status:    response.StatusCode,
		RequestID: sanitize(response.Header.Get("X-Request-Id")),
		Location:  sanitize(response.Header.Get("Location")),
		Method:    method,
		Path:      path,
	}

	if seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); err == nil && seconds > 0 {
		apiErr.RetryAfter = time.Duration(seconds) * time.Second
		if apiErr.RetryAfter > maxRetryAfter {
			apiErr.RetryAfter = maxRetryAfter
		}
	}

	message := ""
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		fields := make(map[string]string)
		for key, value := range payload {
			text, ok := value.(string)
			if !ok || len(fields) >= maxErrorFields {
				continue
			}
			fields[key] = sanitize(text)
		}
		apiErr.Fields = fields

		// "error" is the API's own envelope; the others are accepted so the
		// CLI keeps working when the API adopts RFC 9457 problem details
		// (ADR-034) and for framework-generated errors such as unknown routes.
		for _, key := range []string{"error", "detail", "message", "title"} {
			if fields[key] != "" {
				message = fields[key]
				break
			}
		}
	}

	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	if message == "" {
		message = "unexpected response"
	}
	apiErr.Message = message

	return apiErr
}

// sanitize removes control characters, which a hostile or misconfigured server
// could use to manipulate the terminal, and bounds the length.
func sanitize(text string) string {
	var builder strings.Builder
	count := 0
	for _, r := range strings.TrimSpace(text) {
		if count >= maxMessageRunes {
			builder.WriteString("...")
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		builder.WriteRune(r)
		count++
	}

	return strings.TrimSpace(builder.String())
}
