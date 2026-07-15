package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RotatingWriter wraps a Writer and handles log rotation
type RotatingWriter struct {
	writer   *Writer
	config   Config
	basePath string
	maxSize  int64
	maxFiles int
}

// NewRotatingWriter creates a new rotating writer
func NewRotatingWriter(path string, config Config, redactor *Redactor) (*RotatingWriter, error) {
	if err := secureDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := secureRotatedFiles(path); err != nil {
		return nil, fmt.Errorf("secure rotated files: %w", err)
	}
	w, err := NewWriter(path, redactor)
	if err != nil {
		return nil, err
	}

	return &RotatingWriter{
		writer:   w,
		config:   config,
		basePath: path,
		maxSize:  config.MaxSize,
		maxFiles: config.MaxFiles,
	}, nil
}

// WriteEvent writes an event and rotates if necessary
func (rw *RotatingWriter) WriteEvent(event Event) error {
	// Check if rotation is needed before writing
	if err := rw.checkRotate(); err != nil {
		return fmt.Errorf("rotation check: %w", err)
	}

	return rw.writer.WriteEvent(event)
}

// WriteBytes writes bytes and rotates if necessary
func (rw *RotatingWriter) WriteBytes(data []byte) (int, error) {
	// Check if rotation is needed before writing
	if err := rw.checkRotate(); err != nil {
		return 0, fmt.Errorf("rotation check: %w", err)
	}

	return rw.writer.WriteBytes(data)
}

// checkRotate checks if rotation is needed and performs it
func (rw *RotatingWriter) checkRotate() error {
	size, err := rw.writer.Size()
	if err != nil {
		return err
	}

	if size >= rw.maxSize {
		return rw.rotate()
	}

	return nil
}

// rotate performs the log rotation
func (rw *RotatingWriter) rotate() error {
	if err := secureRotatedFiles(rw.basePath); err != nil {
		return fmt.Errorf("validate rotated files: %w", err)
	}
	// Close current writer
	if err := rw.writer.Close(); err != nil {
		return fmt.Errorf("close current writer: %w", err)
	}

	if rw.maxFiles <= 0 {
		if err := removeRegularFileIfExists(rw.basePath); err != nil {
			return fmt.Errorf("remove current file: %w", err)
		}
	} else {
		if err := removeRegularFileIfExists(rw.rotatedPath(rw.maxFiles)); err != nil {
			return fmt.Errorf("remove oldest rotated file: %w", err)
		}
	}

	// Rotate existing files. Every source and destination is validated as a
	// private regular file before a path mutation occurs.
	for i := rw.maxFiles - 1; i >= 1; i-- {
		oldPath := rw.rotatedPath(i)
		newPath := rw.rotatedPath(i + 1)

		if _, err := os.Lstat(oldPath); err == nil {
			if err := renameRegularFile(oldPath, newPath); err != nil {
				return fmt.Errorf("rotate file %d: %w", i, err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect rotated file %d: %w", i, err)
		}
	}

	if rw.maxFiles > 0 {
		if err := renameRegularFile(rw.basePath, rw.rotatedPath(1)); err != nil {
			return fmt.Errorf("rotate current file: %w", err)
		}
	}

	// Create new writer
	w, err := NewWriter(rw.basePath, rw.writer.redactor)
	if err != nil {
		return fmt.Errorf("create new writer: %w", err)
	}

	rw.writer = w
	return nil
}

// rotatedPath returns the path for a rotated log file
func (rw *RotatingWriter) rotatedPath(n int) string {
	if n == 0 {
		return rw.basePath
	}
	return rw.basePath + "." + fmt.Sprint(n)
}

// Sync flushes the file to disk
func (rw *RotatingWriter) Sync() error {
	return rw.writer.Sync()
}

// Close closes the rotating writer
func (rw *RotatingWriter) Close() error {
	return rw.writer.Close()
}

// Path returns the current file path
func (rw *RotatingWriter) Path() string {
	return rw.writer.Path()
}

// RotatedFiles returns a list of rotated log files
func (rw *RotatingWriter) RotatedFiles() []string {
	var files []string

	// Check for rotated files
	for i := 1; i <= rw.maxFiles; i++ {
		path := rw.rotatedPath(i)
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink == 0 &&
			info.Mode().IsRegular() && diagnosticPermissionsMatch(info, DefaultFileMode) {
			files = append(files, path)
		}
	}

	return files
}

// Cleanup removes rotated files beyond the max limit
func (rw *RotatingWriter) Cleanup() error {
	if err := secureRotatedFiles(rw.basePath); err != nil {
		return err
	}
	files, err := filepath.Glob(rw.basePath + ".*")
	if err != nil {
		return err
	}

	for _, file := range files {
		// Extract the rotation number
		numStr := strings.TrimPrefix(file, rw.basePath+".")
		num, err := strconv.Atoi(numStr)
		if err == nil && num > 0 && strconv.Itoa(num) == numStr && num > rw.maxFiles {
			if err := removeRegularFileIfExists(file); err != nil {
				return err
			}
		}
	}

	return nil
}
