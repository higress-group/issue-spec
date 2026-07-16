package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Writer handles append-only writes to log files
type Writer struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	redactor *Redactor
}

// NewWriter creates a new writer for the given file path
func NewWriter(path string, redactor *Redactor) (*Writer, error) {
	if err := secureDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	file, err := openPrivateAppendFile(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}

	return &Writer{
		file:     file,
		path:     path,
		redactor: redactor,
	}, nil
}

// WriteEvent writes an event to the log file
func (w *Writer) WriteEvent(event Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := verifyOpenFilePath(w.path, w.file); err != nil {
		return err
	}

	// Apply redaction before writing
	if w.redactor != nil {
		event = w.redactor.RedactEvent(event)
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	// Append newline for NDJSON format
	data = append(data, '\n')

	if _, err := w.file.Write(data); err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	return nil
}

// WriteBytes writes raw bytes to the file (for stdout/stderr capture)
func (w *Writer) WriteBytes(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := verifyOpenFilePath(w.path, w.file); err != nil {
		return 0, err
	}

	// Apply redaction before writing
	if w.redactor != nil {
		data = w.redactor.RedactBytes(data)
	}

	n, err := w.file.Write(data)
	if err != nil {
		return n, fmt.Errorf("write bytes: %w", err)
	}

	return n, nil
}

// Sync flushes the file to disk
func (w *Writer) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := verifyOpenFilePath(w.path, w.file); err != nil {
		return err
	}
	return w.file.Sync()
}

// Close closes the writer
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	pathErr := verifyOpenFilePath(w.path, w.file)
	syncErr := w.file.Sync()
	closeErr := w.file.Close()
	return errors.Join(pathErr, syncErr, closeErr)
}

// Path returns the file path
func (w *Writer) Path() string {
	return w.path
}

// Size returns the current size of the log file
func (w *Writer) Size() (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := verifyOpenFilePath(w.path, w.file); err != nil {
		return 0, err
	}

	info, err := w.file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat file: %w", err)
	}

	return info.Size(), nil
}
