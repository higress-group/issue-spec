package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// DefaultMaxSize is the default maximum log file size (10MB)
	DefaultMaxSize = 10 * 1024 * 1024
	// DefaultMaxFiles is the default maximum number of rotated files
	DefaultMaxFiles = 5
	// DefaultRetentionDays is the default log retention duration
	DefaultRetentionDays = 30
	// DefaultRawCaptureKB is the default raw stream capture size (100KB)
	DefaultRawCaptureKB = 100
	// DefaultDirMode is the default directory permissions (0700)
	DefaultDirMode = 0700
	// DefaultFileMode is the default file permissions (0600)
	DefaultFileMode = 0600
)

// Config holds the logging configuration
type Config struct {
	// LogDir is the directory where log files will be written
	LogDir string
	// MaxSize is the maximum size of a log file before rotation
	MaxSize int64
	// MaxFiles is the maximum number of rotated log files to keep
	MaxFiles int
	// RetentionDays is the number of days to retain log files
	RetentionDays int
	// RawCaptureKB is the maximum size of raw stdout/stderr capture in KB
	RawCaptureKB int
}

// DefaultConfig returns a configuration with default values
func DefaultConfig() Config {
	return Config{
		MaxSize:       DefaultMaxSize,
		MaxFiles:      DefaultMaxFiles,
		RetentionDays: DefaultRetentionDays,
		RawCaptureKB:  DefaultRawCaptureKB,
	}
}

// ConfigFromEnv creates a configuration from environment variables
func ConfigFromEnv() Config {
	cfg := DefaultConfig()

	if val := os.Getenv("ISSUE_SPEC_LOG_DIR"); val != "" {
		cfg.LogDir = val
	}
	if val := os.Getenv("ISSUE_SPEC_LOG_MAX_SIZE_MB"); val != "" {
		if mb, err := strconv.Atoi(val); err == nil && mb > 0 {
			cfg.MaxSize = int64(mb) * 1024 * 1024
		}
	}
	if val := os.Getenv("ISSUE_SPEC_LOG_MAX_FILES"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			cfg.MaxFiles = n
		}
	}
	if val := os.Getenv("ISSUE_SPEC_LOG_RETENTION_DAYS"); val != "" {
		if days, err := strconv.Atoi(val); err == nil && days > 0 {
			cfg.RetentionDays = days
		}
	}
	if val := os.Getenv("ISSUE_SPEC_LOG_RAW_CAPTURE_KB"); val != "" {
		if kb, err := strconv.Atoi(val); err == nil && kb > 0 {
			cfg.RawCaptureKB = kb
		}
	}

	return cfg
}

// LogPath returns the path to a log file within the log directory
func (c Config) LogPath(name string) string {
	return filepath.Join(c.LogDir, name)
}

// JobsPath returns the path to the jobs log directory
func (c Config) JobsPath() string {
	return filepath.Join(c.LogDir, "jobs")
}

// JobLogPath returns the path to a job-specific log file
func (c Config) JobLogPath(jobID string) string {
	return filepath.Join(c.JobsPath(), jobID+".ndjson")
}

// JobStdoutPath returns the path to a job's stdout capture file
func (c Config) JobStdoutPath(jobID string) string {
	return filepath.Join(c.JobsPath(), jobID+"-acpx-stdout.log")
}

// JobStderrPath returns the path to a job's stderr capture file
func (c Config) JobStderrPath(jobID string) string {
	return filepath.Join(c.JobsPath(), jobID+"-acpx-stderr.log")
}

// SessionsPath returns the path to the sessions log directory
func (c Config) SessionsPath() string {
	return filepath.Join(c.LogDir, "sessions")
}

// SessionPath returns the path to a session-specific directory
func (c Config) SessionPath(sessionID string) string {
	return filepath.Join(c.SessionsPath(), sessionID)
}

// SessionLogPath returns the path to a session-specific log file
func (c Config) SessionLogPath(sessionID, turnID string) string {
	return filepath.Join(c.SessionPath(sessionID), turnID+".ndjson")
}

// RawCaptureBytes returns the raw capture size in bytes
func (c Config) RawCaptureBytes() int64 {
	return int64(c.RawCaptureKB) * 1024
}

// RetentionDuration returns the retention duration as a time.Duration
func (c Config) RetentionDuration() time.Duration {
	return time.Duration(c.RetentionDays) * 24 * time.Hour
}

// Validate checks the configuration for errors
func (c Config) Validate() error {
	if c.MaxSize <= 0 {
		return fmt.Errorf("invalid max size: %d", c.MaxSize)
	}
	if c.MaxFiles < 0 {
		return fmt.Errorf("invalid max files: %d", c.MaxFiles)
	}
	if c.RetentionDays <= 0 {
		return fmt.Errorf("invalid retention days: %d", c.RetentionDays)
	}
	if c.RawCaptureKB <= 0 {
		return fmt.Errorf("invalid raw capture KB: %d", c.RawCaptureKB)
	}
	return nil
}

// ApplyDefaults sets the log directory to a default path if not set
func (c Config) ApplyDefaults(statePath string) Config {
	if c.LogDir == "" && statePath != "" {
		// Default to sibling "logs" directory beside state file
		stateDir := filepath.Dir(statePath)
		c.LogDir = filepath.Join(stateDir, "logs")
	}
	return c
}
