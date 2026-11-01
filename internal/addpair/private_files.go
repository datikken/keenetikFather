package addpair

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func writeAtomic(path, contents string, mode os.FileMode) (returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if err := temporary.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close temporary file: %w", err))
			}
		}
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary file: %w", err))
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := temporary.WriteString(contents); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return fmt.Errorf("close temporary file: %w", err)
	}
	closed = true
	return os.Rename(temporaryPath, path)
}
