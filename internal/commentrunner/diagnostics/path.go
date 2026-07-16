package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func secureDirectory(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("diagnostic directory path is required")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, DefaultDirMode); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("diagnostic path %s must be a real directory", path)
	}
	file, err := openExistingFileNoFollow(path)
	if err != nil {
		return fmt.Errorf("open diagnostic directory %s: %w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		return fmt.Errorf("diagnostic directory %s changed while opening", path)
	}
	if err := tightenDiagnosticPermissions(file, DefaultDirMode); err != nil {
		return fmt.Errorf("tighten diagnostic directory %s: %w", path, err)
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(opened, after) ||
		!diagnosticPermissionsMatch(after, DefaultDirMode) {
		return fmt.Errorf("diagnostic directory %s is not a stable private directory", path)
	}
	return nil
}

func secureDiagnosticTree(root string) error {
	if err := secureDirectory(root); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read diagnostic directory %s: %w", root, err)
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect diagnostic path %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("diagnostic path %s must not be a symbolic link", path)
		}
		switch {
		case info.IsDir():
			if err := secureDiagnosticTree(path); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := secureExistingRegularFile(path); err != nil {
				return err
			}
		default:
			return fmt.Errorf("diagnostic path %s must be a regular file or real directory", path)
		}
	}
	return nil
}

func openPrivateAppendFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("diagnostic file %s must be a regular non-symlink file", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := openAppendFileNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("open diagnostic file %s: %w", path, err)
	}
	if err := verifyAndTightenOpenFile(path, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func secureExistingRegularFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("diagnostic file %s must be a regular non-symlink file", path)
	}
	file, err := openExistingFileNoFollow(path)
	if err != nil {
		return fmt.Errorf("open existing diagnostic file %s: %w", path, err)
	}
	defer file.Close()
	return verifyAndTightenOpenFile(path, file)
}

func verifyAndTightenOpenFile(path string, file *os.File) error {
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return fmt.Errorf("diagnostic file %s is not a regular file", path)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() ||
		!os.SameFile(opened, pathInfo) {
		return fmt.Errorf("diagnostic file %s changed while opening", path)
	}
	if err := tightenDiagnosticPermissions(file, DefaultFileMode); err != nil {
		return fmt.Errorf("tighten diagnostic file %s: %w", path, err)
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(opened, after) || !diagnosticPermissionsMatch(after, DefaultFileMode) {
		return fmt.Errorf("diagnostic file %s is not a stable private regular file", path)
	}
	return nil
}

func verifyOpenFilePath(path string, file *os.File) error {
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return fmt.Errorf("diagnostic file %s is not a regular file", path)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() ||
		!os.SameFile(opened, pathInfo) || !diagnosticPermissionsMatch(pathInfo, DefaultFileMode) {
		return fmt.Errorf("diagnostic file %s is not the open private regular file", path)
	}
	return nil
}

func secureRotatedFiles(basePath string) error {
	dir := filepath.Dir(basePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefix := filepath.Base(basePath) + "."
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		rotation := strings.TrimPrefix(entry.Name(), prefix)
		index, err := strconv.Atoi(rotation)
		if err != nil || index <= 0 || strconv.Itoa(index) != rotation {
			continue
		}
		if err := secureExistingRegularFile(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func removeRegularFileIfExists(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refuse to remove unsafe diagnostic path %s", path)
	}
	return os.Remove(path)
}

func renameRegularFile(oldPath, newPath string) error {
	if err := secureExistingRegularFile(oldPath); err != nil {
		return err
	}
	if _, err := os.Lstat(newPath); err == nil {
		return fmt.Errorf("diagnostic rotation destination %s already exists", newPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	return secureExistingRegularFile(newPath)
}

func removeDiagnosticPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse to remove diagnostic symlink %s", path)
	}
	if info.Mode().IsRegular() {
		return removeRegularFileIfExists(path)
	}
	if !info.IsDir() {
		return fmt.Errorf("refuse to remove special diagnostic path %s", path)
	}
	if err := secureDirectory(path); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeDiagnosticPath(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	// Recheck the final path immediately before removal. os.Remove never walks
	// a replacement directory or follows a replacement symlink.
	info, err = os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("diagnostic directory %s changed before removal", path)
	}
	return os.Remove(path)
}
