package diagnostics

import (
	"sync"
)

// BoundedWriter wraps a Writer and enforces a maximum size limit
type BoundedWriter struct {
	writer    *Writer
	maxBytes  int64
	written   int64
	truncated bool
	mu        sync.Mutex
}

// NewBoundedWriter creates a new bounded writer
func NewBoundedWriter(writer *Writer, maxBytes int64) *BoundedWriter {
	return &BoundedWriter{
		writer:   writer,
		maxBytes: maxBytes,
	}
}

// WriteBytes writes bytes to the file, truncating if the limit is exceeded
func (bw *BoundedWriter) WriteBytes(data []byte) (int, error) {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	if bw.truncated {
		return len(data), nil // Silently drop data after truncation
	}

	remaining := bw.maxBytes - bw.written
	if remaining <= 0 {
		bw.truncated = true
		// Write truncation marker
		marker := []byte("\n\n[TRUNCATED: original size exceeded capture limit]\n")
		bw.writer.WriteBytes(marker)
		return len(data), nil
	}

	if int64(len(data)) > remaining {
		// Write partial data
		n, err := bw.writer.WriteBytes(data[:remaining])
		bw.written += int64(n)

		// Mark as truncated and write marker
		bw.truncated = true
		marker := []byte("\n\n[TRUNCATED: original size exceeded capture limit]\n")
		bw.writer.WriteBytes(marker)

		return len(data), err
	}

	n, err := bw.writer.WriteBytes(data)
	bw.written += int64(n)
	return n, err
}

// Sync flushes the underlying writer
func (bw *BoundedWriter) Sync() error {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	return bw.writer.Sync()
}

// Close closes the bounded writer
func (bw *BoundedWriter) Close() error {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	return bw.writer.Close()
}

// IsTruncated returns whether the output was truncated
func (bw *BoundedWriter) IsTruncated() bool {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	return bw.truncated
}

// Written returns the number of bytes written
func (bw *BoundedWriter) Written() int64 {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	return bw.written
}

// Path returns the underlying file path
func (bw *BoundedWriter) Path() string {
	return bw.writer.Path()
}
