package sandbox

import (
	"context"
	"path/filepath"
	"testing"
)

// scratchConfig builds an unsafe-mode config with all four job scratch dirs
// below root.
func scratchConfig(root string) Config {
	return Config{
		UnsafeNoSandbox:   true,
		WorkspacePath:     filepath.Join(root, "workspace"),
		TempHome:          filepath.Join(root, "home"),
		TempGHConfigDir:   filepath.Join(root, "gh"),
		TempXDGConfigHome: filepath.Join(root, "xdg"),
		TempCodexHome:     filepath.Join(root, "codex"),
		JobTmpDir:         filepath.Join(root, "scratch", "tmp"),
		JobGoTmpDir:       filepath.Join(root, "scratch", "go-tmp"),
		JobXDGDataHome:    filepath.Join(root, "scratch", "xdg-data"),
		JobXDGStateHome:   filepath.Join(root, "scratch", "xdg-state"),
		HostEnv:           []string{"PATH=/usr/bin", "TMPDIR=/host/tmp"},
		EnvAllowlist:      []string{"PATH"},
	}
}

func TestPrepareUnsafeSetsJobScratchEnv(t *testing.T) {
	root := t.TempDir()
	cfg := scratchConfig(root)
	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "acpx"}, Dependencies{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	env := envMap(prepared.Command.Env)
	for name, want := range map[string]string{
		"TMPDIR":         cfg.JobTmpDir,
		"GOTMPDIR":       cfg.JobGoTmpDir,
		"XDG_DATA_HOME":  cfg.JobXDGDataHome,
		"XDG_STATE_HOME": cfg.JobXDGStateHome,
	} {
		if got := env[name]; got != want {
			t.Fatalf("%s = %q, want %q in env %v", name, got, want, prepared.Command.Env)
		}
	}
	meta := prepared.Metadata.Env
	if meta.TmpDir != cfg.JobTmpDir || meta.GoTmpDir != cfg.JobGoTmpDir ||
		meta.XDGDataHome != cfg.JobXDGDataHome || meta.XDGStateHome != cfg.JobXDGStateHome {
		t.Fatalf("scratch metadata missing: %+v", meta)
	}
}

func TestPrepareWithoutJobScratchLeavesEnvUntouched(t *testing.T) {
	root := t.TempDir()
	cfg := scratchConfig(root)
	cfg.JobTmpDir, cfg.JobGoTmpDir, cfg.JobXDGDataHome, cfg.JobXDGStateHome = "", "", "", ""
	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "acpx"}, Dependencies{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	env := envMap(prepared.Command.Env)
	for _, name := range []string{"TMPDIR", "GOTMPDIR", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		if _, ok := env[name]; ok {
			t.Fatalf("%s must stay unset without a job scratch dir: %v", name, prepared.Command.Env)
		}
	}
	meta := prepared.Metadata.Env
	if meta.TmpDir != "" || meta.GoTmpDir != "" || meta.XDGDataHome != "" || meta.XDGStateHome != "" {
		t.Fatalf("scratch metadata must stay empty: %+v", meta)
	}
}

func TestPrepareJobScratchEnvSurvivesCommandEnvOverride(t *testing.T) {
	root := t.TempDir()
	cfg := scratchConfig(root)
	commandEnv := []string{
		"TMPDIR=/evil/tmp",
		"GOTMPDIR=/evil/go-tmp",
		"XDG_DATA_HOME=/evil/data",
		"XDG_STATE_HOME=/evil/state",
	}
	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "acpx", Env: commandEnv}, Dependencies{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	env := envMap(prepared.Command.Env)
	for name, want := range map[string]string{
		"TMPDIR":         cfg.JobTmpDir,
		"GOTMPDIR":       cfg.JobGoTmpDir,
		"XDG_DATA_HOME":  cfg.JobXDGDataHome,
		"XDG_STATE_HOME": cfg.JobXDGStateHome,
	} {
		if got := env[name]; got != want {
			t.Fatalf("command env overrode protected %s: got %q, want %q", name, got, want)
		}
	}
}

func TestWritableBindsRejectJobScratchOverlap(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	cfg := scratchConfig(root)
	cfg.UnsafeNoSandbox = false
	cfg.WorkspacePath = workspace
	for _, dir := range []string{workspace, cfg.JobTmpDir, cfg.JobGoTmpDir, cfg.JobXDGDataHome, cfg.JobXDGStateHome} {
		if err := mkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"tmp", "go-tmp", "xdg-data", "xdg-state"} {
		bind := filepath.Join(root, "scratch", name)
		if _, err := validatedWritableBinds(Config{
			WorkspacePath:   workspace,
			JobTmpDir:       cfg.JobTmpDir,
			JobGoTmpDir:     cfg.JobGoTmpDir,
			JobXDGDataHome:  cfg.JobXDGDataHome,
			JobXDGStateHome: cfg.JobXDGStateHome,
			WritableBinds:   []string{bind},
		}); err == nil {
			t.Fatalf("writable bind overlapping job scratch %s must be rejected", name)
		}
	}
	// The scratch parent overlaps every scratch dir and must also be rejected.
	if _, err := validatedWritableBinds(Config{
		WorkspacePath:   workspace,
		JobTmpDir:       cfg.JobTmpDir,
		JobGoTmpDir:     cfg.JobGoTmpDir,
		JobXDGDataHome:  cfg.JobXDGDataHome,
		JobXDGStateHome: cfg.JobXDGStateHome,
		WritableBinds:   []string{filepath.Join(root, "scratch")},
	}); err == nil {
		t.Fatalf("writable bind covering the job scratch root must be rejected")
	}
}
