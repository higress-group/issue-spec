package diagnostics

import (
	"os"
	"path/filepath"
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
		JobID:     "job-123",
		CycleID:   "cycle-abc",
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
		LogDir:  tmpDir,
		MaxSize: 100, // Small size to trigger rotation
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
