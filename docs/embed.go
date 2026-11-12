// Package docs embeds the generated API specification for serving at runtime.
package docs

import (
	_ "embed"
	"log/slog"
	"net/http"
)

//go:embed swagger.json
var specification []byte

// SpecHandler serves the checked-in API contract without reading files from the
// process working directory.
func SpecHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(specification); err != nil {
			slog.Debug("failed to write API specification response",
				"error", err,
			)
		}
	})
}
