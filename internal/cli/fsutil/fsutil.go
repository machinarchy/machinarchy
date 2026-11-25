// Package fsutil holds the one file-writing routine the CLI uses for files
// that contain, or sit next to, credentials.
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// WriteAtomic replaces the file at path with data. The directory is created
// with dirMode if it is missing. The data is written to a temporary file in
// the same directory, created with fileMode before anything is written to it,
// synced, and renamed over the target, so a crash or a second process never
// leaves a half-written or world-readable file behind.
func WriteAtomic(path string, data []byte, dirMode, fileMode fs.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, dirMode); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	file, err := os.CreateTemp(directory, ".machinarchy-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempPath := file.Name()
	defer func() {
		if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to remove temporary file", "path", tempPath, "error", err)
		}
	}()

	writeErr := func() (retErr error) {
		defer func() {
			if err := file.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close temporary file: %w", err))
			}
		}()

		if err := file.Chmod(fileMode); err != nil {
			return fmt.Errorf("secure temporary file: %w", err)
		}
		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("write file: %w", err)
		}

		return file.Sync()
	}()
	if writeErr != nil {
		return writeErr
	}

	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}

	return nil
}
