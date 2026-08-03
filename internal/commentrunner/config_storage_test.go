package commentrunner

import (
	"path/filepath"
	"testing"
	"time"
)

func storageValidateBase() Config {
	return Config{
		Repositories: []string{"o/r"}, RunnerIdentity: "bot", StatePath: filepath.Join("/tmp", "state.json"),
		WorkspaceRoot: "/tmp/ws", PollInterval: NewDuration(time.Minute), FallbackInterval: NewDuration(time.Hour),
		WorkspaceRetention: NewDuration(24 * time.Hour), MaxConcurrentJobs: 1, Agent: DefaultAgentConfig(),
	}
}

func TestStorageConfigDefaults(t *testing.T) {
	cfg, err := DefaultConfigFromEnv()
	if err != nil {
		t.Fatalf("DefaultConfigFromEnv: %v", err)
	}
	if cfg.StorageMinFreeBytes != 0 {
		t.Fatalf("StorageMinFreeBytes default = %d, want disabled (0)", cfg.StorageMinFreeBytes)
	}
	if cfg.StorageOrphanGrace.Duration != 7*24*time.Hour {
		t.Fatalf("StorageOrphanGrace default = %s, want 168h", cfg.StorageOrphanGrace.Duration)
	}
}

func TestStorageConfigValidate(t *testing.T) {
	base := storageValidateBase()
	base.StorageOrphanGrace = NewDuration(7 * 24 * time.Hour)
	if err := base.Validate(); err != nil {
		t.Fatalf("valid storage config rejected: %v", err)
	}
	base.StorageMinFreeBytes = 1 << 30
	if err := base.Validate(); err != nil {
		t.Fatalf("valid min free bytes rejected: %v", err)
	}
	negative := storageValidateBase()
	negative.StorageMinFreeBytes = -1
	if err := negative.Validate(); err == nil {
		t.Fatalf("negative storage_min_free_bytes must be rejected")
	}
	negativeGrace := storageValidateBase()
	negativeGrace.StorageOrphanGrace = NewDuration(-time.Hour)
	if err := negativeGrace.Validate(); err == nil {
		t.Fatalf("negative storage_orphan_grace must be rejected")
	}
}
