package utils

import (
	"fmt"
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

// CloseWithError closes a resource and returns a wrapped error if closing fails.
func CloseWithError(closer io.Closer, resource string) error {
	if err := closer.Close(); err != nil {
		return fmt.Errorf("close %s: %w", resource, err)
	}

	return nil
}
