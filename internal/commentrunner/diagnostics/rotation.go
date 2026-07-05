package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RotatingWriter wraps a Writer and handles log rotation
type RotatingWriter struct {
	writer    *Writer
	config    Config
	basePath  string
	maxSize   int64
	maxFiles  int
}

// NewRotatingWriter creates a new rotating writer
func NewRotatingWriter(path string, config Config, redactor *Redactor) (*RotatingWriter, error) {
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
	// Close current writer
	if err := rw.writer.Close(); err != nil {
		return fmt.Errorf("close current writer: %w", err)
	}

	// Rotate existing files
	for i := rw.maxFiles - 1; i >= 1; i-- {
		oldPath := rw.rotatedPath(i)
		newPath := rw.rotatedPath(i + 1)

		if i == rw.maxFiles-1 {
			// Delete the oldest file
			os.Remove(newPath)
		}

		if _, err := os.Stat(oldPath); err == nil {
			if err := os.Rename(oldPath, newPath); err != nil {
				return fmt.Errorf("rotate file %d: %w", i, err)
			}
		}
	}

	// Rename current file to .1
	if err := os.Rename(rw.basePath, rw.rotatedPath(1)); err != nil {
		return fmt.Errorf("rotate current file: %w", err)
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
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		}
	}

	return files
}

// Cleanup removes rotated files beyond the max limit
func (rw *RotatingWriter) Cleanup() error {
	files, err := filepath.Glob(rw.basePath + ".*")
	if err != nil {
		return err
	}

	for _, file := range files {
		// Extract the rotation number
		numStr := strings.TrimPrefix(file, rw.basePath+".")
		var num int
		if _, err := fmt.Sscanf(numStr, "%d", &num); err == nil {
			if num > rw.maxFiles {
				os.Remove(file)
			}
		}
	}

	return nil
}
