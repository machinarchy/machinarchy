package api

import (
	"net/http"
	"testing"
	"unicode"
)

// FuzzNewError feeds arbitrary response bodies and headers to the API error
// decoder: no panic, bounded fields, no control characters in anything that
// is later printed (T8, T10).
func FuzzNewError(f *testing.F) {
	f.Add(`{"error":"nope"}`, "req-1", "5", 400)
	f.Add(`{"detail":"x","title":1,"nested":{"a":[1]}}`, "", "-1", 500)
	f.Add("<html>\x1b[31mred\x1b[0m</html>", "id\x1b]0;x\x07", "999999999999999999999", 502)
	f.Add(`[1,2`, "", "", 0)
	f.Add(`{"error":"\u001b[2J\u0000"}`, "\n", "3", 403)

	f.Fuzz(func(t *testing.T, body, requestID, retryAfter string, status int) {
		response := &http.Response{StatusCode: status, Header: http.Header{}}
		response.Header.Set("X-Request-Id", requestID)
		response.Header.Set("Retry-After", retryAfter)

		apiErr := newError(response, http.MethodGet, "/api/v1/x", []byte(body))
		if apiErr == nil {
			t.Fatal("nil error")
		}
		if apiErr.RetryAfter < 0 || apiErr.RetryAfter > maxRetryAfter {
			t.Fatalf("retry-after out of range: %v", apiErr.RetryAfter)
		}
		if len(apiErr.Fields) > maxErrorFields {
			t.Fatalf("%d fields", len(apiErr.Fields))
		}
		for _, text := range append([]string{apiErr.Message, apiErr.RequestID, apiErr.Error()}, fieldValues(apiErr)...) {
			for _, r := range text {
				if unicode.IsControl(r) && r != '\n' {
					t.Fatalf("control character %U in %q", r, text)
				}
			}
		}
	})
}

// FuzzDecodeResponse feeds arbitrary bodies to the success decoder: it may
// fail but never panics.
func FuzzDecodeResponse(f *testing.F) {
	f.Add(200, `{"items":[]}`)
	f.Add(204, ``)
	f.Add(200, `{"items":{"a":1}}`)
	f.Add(200, "\x00\xff")

	f.Fuzz(func(_ *testing.T, status int, body string) {
		var out struct {
			Items []string `json:"items"`
		}
		_ = decodeResponse(status, "/api/v1/x", []byte(body), &out)
	})
}

func fieldValues(e *Error) []string {
	out := make([]string, 0, len(e.Fields))
	for _, v := range e.Fields {
		out = append(out, v)
	}

	return out
}
