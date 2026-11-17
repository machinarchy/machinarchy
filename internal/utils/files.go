package utils

import (
	"io"
	"log/slog"
)

// CloseWithLog closes a resource and logs any error.
func CloseWithLog(closer io.Closer, resource string) {
	if err := closer.Close(); err != nil {
		slog.Warn("failed to close resource",
			"resource", resource,
			"error", err,
		)
	}
}
