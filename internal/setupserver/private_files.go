package setupserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func writePrivateFile(path, contents string) (returnErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s уже существует; отказываюсь его перезаписывать", path)
		}
		return err
	}
	closed := false
	committed := false
	defer func() {
		if !closed {
			if err := file.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close private file: %w", err))
			}
		}
		if !committed {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove incomplete private file: %w", err))
			}
		}
	}()
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("set private file permissions: %w", err)
	}
	if _, err := file.WriteString(contents); err != nil {
		return fmt.Errorf("write private file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private file: %w", err)
	}
	if err := file.Close(); err != nil {
		closed = true
		return fmt.Errorf("close private file: %w", err)
	}
	closed = true
	committed = true
	return nil
}

func archiveExistingConfig(root string, now time.Time) (backupPath string, returnErr error) {
	configDir := filepath.Join(root, "config")
	backupRoot := filepath.Join(configDir, backupDirName)
	paths := []string{
		filepath.Join(configDir, "server.conf"),
		filepath.Join(configDir, "radio-peer.conf"),
		filepath.Join(configDir, "module-peer.conf"),
		filepath.Join(configDir, "pairs"),
	}
	var existing []string
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			existing = append(existing, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("проверить %s: %w", path, err)
		}
	}
	if len(existing) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(backupRoot, 0700); err != nil {
		return "", fmt.Errorf("создать каталог резервных копий: %w", err)
	}
	if err := os.Chmod(backupRoot, 0700); err != nil {
		return "", fmt.Errorf("ограничить доступ к резервным копиям: %w", err)
	}
	backupPath = filepath.Join(backupRoot, now.Format("20060102-150405.000000000"))
	if err := os.Mkdir(backupPath, 0700); err != nil {
		return "", fmt.Errorf("создать резервную копию %s: %w", backupPath, err)
	}
	type movedPath struct {
		source string
		target string
	}
	var moved []movedPath
	for _, path := range existing {
		target := filepath.Join(backupPath, filepath.Base(path))
		if err := os.Rename(path, target); err != nil {
			returnErr = fmt.Errorf("переместить %s в резервную копию %s: %w", path, backupPath, err)
			for index := len(moved) - 1; index >= 0; index-- {
				if rollbackErr := os.Rename(moved[index].target, moved[index].source); rollbackErr != nil {
					returnErr = errors.Join(returnErr, fmt.Errorf("восстановить %s: %w", moved[index].source, rollbackErr))
				}
			}
			if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("удалить неполную резервную копию %s: %w", backupPath, err))
			}
			return backupPath, returnErr
		}
		moved = append(moved, movedPath{source: path, target: target})
	}
	return backupPath, nil
}

func fileExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
