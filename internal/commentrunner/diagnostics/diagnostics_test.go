package diagnostics

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaxSize != DefaultMaxSize {
		t.Errorf("expected MaxSize %d, got %d", DefaultMaxSize, cfg.MaxSize)
	}

	if cfg.MaxFiles != DefaultMaxFiles {
		t.Errorf("expected MaxFiles %d, got %d", DefaultMaxFiles, cfg.MaxFiles)
	}

	if cfg.RetentionDays != DefaultRetentionDays {
		t.Errorf("expected RetentionDays %d, got %d", DefaultRetentionDays, cfg.RetentionDays)
	}

	if cfg.RawCaptureKB != DefaultRawCaptureKB {
		t.Errorf("expected RawCaptureKB %d, got %d", DefaultRawCaptureKB, cfg.RawCaptureKB)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     DefaultConfig(),
			wantErr: false,
		},
		{
			name: "invalid max size",
			cfg: Config{
				MaxSize:       0,
				MaxFiles:      DefaultMaxFiles,
				RetentionDays: DefaultRetentionDays,
				RawCaptureKB:  DefaultRawCaptureKB,
			},
			wantErr: true,
		},
		{
			name: "invalid negative max files",
			cfg: Config{
				MaxSize:       DefaultMaxSize,
				MaxFiles:      -1,
				RetentionDays: DefaultRetentionDays,
				RawCaptureKB:  DefaultRawCaptureKB,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigPaths(t *testing.T) {
	cfg := Config{
		LogDir: "/tmp/test-logs",
	}

	if got := cfg.LogPath("test.ndjson"); got != "/tmp/test-logs/test.ndjson" {
		t.Errorf("expected /tmp/test-logs/test.ndjson, got %s", got)
	}

	if got := cfg.JobsPath(); got != "/tmp/test-logs/jobs" {
		t.Errorf("expected /tmp/test-logs/jobs, got %s", got)
	}

	if got := cfg.JobLogPath("job-123"); got != "/tmp/test-logs/jobs/job-123.ndjson" {
		t.Errorf("expected /tmp/test-logs/jobs/job-123.ndjson, got %s", got)
	}

	if got := cfg.SessionPath("session-abc"); got != "/tmp/test-logs/sessions/session-abc" {
		t.Errorf("expected /tmp/test-logs/sessions/session-abc, got %s", got)
	}
}

func TestRedactorBasic(t *testing.T) {
	r := NewRedactor()

	// Test GitHub token redaction
	input := "token: ghp_1234567890abcdefghijklmnopqrstuvwxyz123456"
	got := r.RedactString(input)
	if got == input {
		t.Error("expected token to be redacted")
	}

	// Test bearer token redaction
	input = "Authorization: Bearer abcdefghijklmnopqrstuvwxyz1234567890ab"
	got = r.RedactString(input)
	if got == input {
		t.Error("expected bearer token to be redacted")
	}
}

func TestRedactorKnownTokens(t *testing.T) {
	r := NewRedactor()

	// Add a known token
	testToken := "secret-token-value-12345"
	r.AddToken(testToken, "[REDACTED:test]")

	input := "The token is: " + testToken
	got := r.RedactString(input)

	if got == input {
		t.Error("expected known token to be redacted")
	}

	if !contains(got, "[REDACTED:test]") {
		t.Error("expected redaction marker")
	}
}

func TestEventCreation(t *testing.T) {
	e := NewEvent(LevelInfo, "test-component", "test-event", "test message")

	if e.SchemaVersion != SchemaVersion {
		t.Errorf("expected schema version %d, got %d", SchemaVersion, e.SchemaVersion)
	}

	if e.Level != LevelInfo {
		t.Errorf("expected level info, got %s", e.Level)
	}

	if e.Component != "test-component" {
		t.Errorf("expected component test-component, got %s", e.Component)
	}

	if e.Event != "test-event" {
		t.Errorf("expected event test-event, got %s", e.Event)
	}

	if e.Message != "test message" {
		t.Errorf("expected message 'test message', got %s", e.Message)
	}
}

func TestEventWithMethods(t *testing.T) {
	correlation := Correlation{
		JobID:           "job-123",
		CycleID:         "cycle-abc",
		PublicSessionID: "session-xyz",
	}

	e := NewEvent(LevelWarn, "component", "event", "message").
		WithScope("github.com", "owner/repo", "runner").
		WithProcessID(1234).
		WithCorrelation(correlation).
		WithDetail("key", "value").
		WithRedaction()

	if e.RunnerScope.Host != "github.com" {
		t.Errorf("expected host github.com, got %s", e.RunnerScope.Host)
	}

	if e.ProcessID != 1234 {
		t.Errorf("expected process ID 1234, got %d", e.ProcessID)
	}

	if e.Correlation.JobID != "job-123" {
		t.Errorf("expected job ID job-123, got %s", e.Correlation.JobID)
	}

	if e.Details["key"] != "value" {
		t.Error("expected detail key to be value")
	}

	if e.RedactionStatus != "redacted" {
		t.Errorf("expected redaction status redacted, got %s", e.RedactionStatus)
	}
}

func TestStoreCreation(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		LogDir:        tmpDir,
		MaxSize:       1024,
		MaxFiles:      3,
		RetentionDays: 7,
		RawCaptureKB:  50,
	}

	store, err := NewStore(cfg, NewRedactor())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	// Check that directories were created
	jobsPath := store.Config().JobsPath()
	info, err := os.Stat(jobsPath)
	if err != nil {
		t.Errorf("jobs directory not created: %v", err)
	}

	if !info.IsDir() {
		t.Error("jobs path is not a directory")
	}

	sessionsPath := store.Config().SessionsPath()
	info, err = os.Stat(sessionsPath)
	if err != nil {
		t.Errorf("sessions directory not created: %v", err)
	}

	if !info.IsDir() {
		t.Error("sessions path is not a directory")
	}
}

func TestWriterAndRotation(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.ndjson")

	cfg := Config{
		LogDir:   tmpDir,
		MaxSize:  100, // Small size to trigger rotation
		MaxFiles: 2,
	}

	rw, err := NewRotatingWriter(logPath, cfg, NewRedactor())
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	defer rw.Close()

	// Write events until rotation
	for i := 0; i < 10; i++ {
		e := NewEvent(LevelInfo, "test", "event", "message").
			WithDetail("iteration", i)
		if err := rw.WriteEvent(e); err != nil {
			t.Errorf("WriteEvent() error = %v", err)
		}
	}

	// Check that rotated files exist
	rotated := rw.RotatedFiles()
	if len(rotated) == 0 {
		t.Error("expected rotated files to exist")
	}
}

func TestBoundedWriter(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bounded.log")

	w, err := NewWriter(logPath, NewRedactor())
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	bw := NewBoundedWriter(w, 100) // 100 bytes limit

	// Write data that fits
	data1 := []byte("short data")
	n, err := bw.WriteBytes(data1)
	if err != nil {
		t.Errorf("WriteBytes() error = %v", err)
	}
	if n != len(data1) {
		t.Errorf("expected to write %d bytes, got %d", len(data1), n)
	}

	// Write data that will exceed limit
	longData := make([]byte, 200)
	for i := range longData {
		longData[i] = 'x'
	}
	n, err = bw.WriteBytes(longData)
	if err != nil {
		t.Errorf("WriteBytes() error = %v", err)
	}
	// Should report writing all bytes even if truncated
	if n != len(longData) {
		t.Errorf("expected to write %d bytes, got %d", len(longData), n)
	}

	if !bw.IsTruncated() {
		t.Error("expected writer to be truncated")
	}

	bw.Close()

	// Check file content
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	// File should contain truncation marker
	if !contains(string(content), "[TRUNCATED:") {
		t.Error("expected truncation marker in file")
	}
}

func TestConfigApplyDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	cfg := Config{}
	cfg = cfg.ApplyDefaults(statePath)

	expectedDir := filepath.Join(tmpDir, "logs")
	if cfg.LogDir != expectedDir {
		t.Errorf("expected log dir %s, got %s", expectedDir, cfg.LogDir)
	}
}

func TestBoundedWriterPreservesCaptureLimitAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs", "job-acpx-stdout.log")
	firstWriter, err := NewWriter(path, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	first := NewBoundedWriter(firstWriter, 8)
	if _, err := first.WriteBytes([]byte("123456")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	secondWriter, err := NewWriter(path, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	second := NewBoundedWriter(secondWriter, 8)
	if second.Written() != 6 {
		t.Fatalf("restart written=%d want=6", second.Written())
	}
	if _, err := second.WriteBytes([]byte("7890")); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	thirdWriter, err := NewWriter(path, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	third := NewBoundedWriter(thirdWriter, 8)
	if _, err := third.WriteBytes([]byte("must-not-append")); err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !strings.HasPrefix(string(after), "12345678") ||
		strings.Count(string(after), "[TRUNCATED:") != 1 {
		t.Fatalf("restart capture=%q", after)
	}
}

func TestBoundedWriterRedactsBeforeTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-acpx-stderr.log")
	writer, err := NewWriter(path, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	bounded := NewBoundedWriter(writer, 24)
	if _, err := bounded.WriteBytes([]byte("1234567890token=abcdefghijklmnopqrstuvwxyz0123456789")); err != nil {
		t.Fatal(err)
	}
	if err := bounded.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("abcdefgh")) || !bytes.Contains(data, []byte("[REDACTED")) {
		t.Fatalf("bounded capture leaked a truncated secret: %q", data)
	}
}

func TestLoggerRestartAppendsLifecycleWithPrivatePermissions(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	cfg := Config{LogDir: logDir, MaxSize: 1 << 20, MaxFiles: 2, RetentionDays: 30, RawCaptureKB: 1}
	for _, event := range []string{"first_start", "restart_start"} {
		logger, err := NewLogger(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := logger.WriteEventWithCorrelation(LevelInfo, "runner", event, "runner lifecycle",
			Correlation{CycleID: event}, nil); err != nil {
			t.Fatal(err)
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(logDir, "runner.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"event":"first_start"`)) ||
		!bytes.Contains(data, []byte(`"event":"restart_start"`)) {
		t.Fatalf("restart log=%s", data)
	}
	if info, err := os.Stat(logDir); err != nil || !diagnosticPermissionsMatch(info, DefaultDirMode) {
		t.Fatalf("log dir mode info=%v err=%v", info, err)
	}
	if info, err := os.Stat(filepath.Join(logDir, "runner.ndjson")); err != nil || !diagnosticPermissionsMatch(info, DefaultFileMode) {
		t.Fatalf("runner log mode info=%v err=%v", info, err)
	}
}

func TestLoggerRestartTightensExistingDiagnosticTree(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	directories := []string{
		logDir,
		filepath.Join(logDir, "jobs"),
		filepath.Join(logDir, "sessions"),
		filepath.Join(logDir, "sessions", "session-old"),
	}
	for _, path := range directories {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{
		filepath.Join(logDir, "runner.ndjson"),
		filepath.Join(logDir, "runner.ndjson.1"),
		filepath.Join(logDir, "errors.ndjson"),
		filepath.Join(logDir, "errors.ndjson.1"),
		filepath.Join(logDir, "index.ndjson"),
		filepath.Join(logDir, "jobs", "job-old.ndjson"),
		filepath.Join(logDir, "jobs", "job-old.ndjson.1"),
		filepath.Join(logDir, "jobs", "job-old-acpx-stdout.log"),
		filepath.Join(logDir, "jobs", "job-old-acpx-stderr.log"),
		filepath.Join(logDir, "sessions", "session-old", "turn-old.ndjson"),
		filepath.Join(logDir, "sessions", "session-old", "turn-old.ndjson.1"),
	}
	for _, path := range files {
		if err := os.WriteFile(path, []byte("existing\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	logger, err := NewLogger(Config{LogDir: logDir, MaxSize: 1 << 20, MaxFiles: 2, RetentionDays: 30, RawCaptureKB: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range directories {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !diagnosticPermissionsMatch(info, DefaultDirMode) {
			t.Fatalf("directory %s info=%v err=%v", path, info, err)
		}
	}
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !diagnosticPermissionsMatch(info, DefaultFileMode) {
			t.Fatalf("file %s info=%v err=%v", path, info, err)
		}
	}
}

func TestLoggerRejectsSymlinksInDiagnosticTree(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(string) string
	}{
		{name: "runner file", path: func(root string) string { return filepath.Join(root, "runner.ndjson") }},
		{name: "rotated file", path: func(root string) string { return filepath.Join(root, "runner.ndjson.1") }},
		{name: "job file", path: func(root string) string { return filepath.Join(root, "jobs", "job-old.ndjson") }},
		{name: "session file", path: func(root string) string { return filepath.Join(root, "sessions", "session-old", "turn-old.ndjson") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			logDir := filepath.Join(base, "logs")
			path := test.path(logDir)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(base, "outside.log")
			if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			logger, err := NewLogger(Config{LogDir: logDir, MaxSize: 1 << 20, MaxFiles: 2, RetentionDays: 30, RawCaptureKB: 1})
			if logger != nil {
				_ = logger.Close()
			}
			if err == nil {
				t.Fatal("diagnostic symlink accepted")
			}
			data, readErr := os.ReadFile(target)
			if readErr != nil || string(data) != "outside\n" {
				t.Fatalf("symlink target changed: %q err=%v", data, readErr)
			}
		})
	}
}

func TestLoggerRejectsSymlinkDirectories(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(string) string
	}{
		{name: "log root", path: func(root string) string { return root }},
		{name: "jobs directory", path: func(root string) string { return filepath.Join(root, "jobs") }},
		{name: "sessions directory", path: func(root string) string { return filepath.Join(root, "sessions") }},
		{name: "session directory", path: func(root string) string { return filepath.Join(root, "sessions", "session-old") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			logDir := filepath.Join(base, "logs")
			path := test.path(logDir)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(base, "outside")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			logger, err := NewLogger(Config{LogDir: logDir, MaxSize: 1 << 20, MaxFiles: 2, RetentionDays: 30, RawCaptureKB: 1})
			if logger != nil {
				_ = logger.Close()
			}
			if err == nil {
				t.Fatal("diagnostic directory symlink accepted")
			}
			info, statErr := os.Lstat(path)
			if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("directory symlink changed: info=%v err=%v", info, statErr)
			}
		})
	}
}

func TestStoreCleanupRejectsLateSymlink(t *testing.T) {
	base := t.TempDir()
	logDir := filepath.Join(base, "logs")
	store, err := NewStore(Config{LogDir: logDir, MaxSize: 1 << 20, MaxFiles: 2, RetentionDays: 30, RawCaptureKB: 1}, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "outside.log")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(logDir, "jobs", "late.ndjson")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := store.Cleanup(); err == nil {
		t.Fatal("cleanup accepted late diagnostic symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "outside\n" {
		t.Fatalf("cleanup target changed: %q err=%v", data, err)
	}
}

func TestWriterRejectsLateSymlinkReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runner.ndjson")
	writer, err := NewWriter(path, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.log")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := writer.WriteBytes([]byte("unsafe\n")); err == nil {
		t.Fatal("writer accepted late symlink replacement")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "outside\n" {
		t.Fatalf("writer target changed: %q err=%v", data, err)
	}
	_ = writer.Close()
}

func TestRotatingWriterRejectsLateSymlinkDestination(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runner.ndjson")
	writer, err := NewRotatingWriter(path, Config{LogDir: root, MaxSize: 1, MaxFiles: 2}, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteEvent(NewEvent(LevelInfo, "runner", "first", "first")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.log")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".1"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := writer.WriteEvent(NewEvent(LevelInfo, "runner", "second", "second")); err == nil {
		t.Fatal("rotation accepted symlink destination")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "outside\n" {
		t.Fatalf("rotation target changed: %q err=%v", data, err)
	}
	_ = writer.Close()
}

func TestRotatingWriterCleanupRejectsLateSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runner.ndjson")
	writer, err := NewRotatingWriter(path, Config{LogDir: root, MaxSize: 1 << 20, MaxFiles: 2}, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.log")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".3"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := writer.Cleanup(); err == nil {
		t.Fatal("rotating cleanup accepted late symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "outside\n" {
		t.Fatalf("rotating cleanup target changed: %q err=%v", data, err)
	}
	_ = writer.Close()
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
