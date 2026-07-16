//go:build linux

package diagnostics

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLoggerRejectsSpecialFileInDiagnosticTree(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(logDir, "runner.ndjson"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, err := NewLogger(Config{LogDir: logDir, MaxSize: 1 << 20, MaxFiles: 2, RetentionDays: 30, RawCaptureKB: 1})
	if logger != nil {
		_ = logger.Close()
	}
	if err == nil {
		t.Fatal("special diagnostic file accepted")
	}
}
