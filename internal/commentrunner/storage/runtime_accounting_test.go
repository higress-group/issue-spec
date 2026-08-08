package storage

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRuntimeCacheDirsOrder(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "scope", "home")
	got := RuntimeCacheDirs(home)
	want := []string{
		filepath.Join(home, ".npm", "_npx"),
		filepath.Join(home, ".npm"),
		filepath.Join(home, ".cache"),
		filepath.Join(home, "go", "pkg", "mod"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RuntimeCacheDirs = %v, want %v", got, want)
	}
}

func TestMeasureRuntimeHomeClassification(t *testing.T) {
	root := t.TempDir()
	protected := map[string]int{
		"scope.json":                     100,
		"gh/hosts.yml":                   10,
		"xdg/config.toml":                20,
		"codex/sessions/s1.json":         30,
		"acpx-runtime/state.json":        40,
		"home/.acpx/sessions/index.json": 50,
		"home/.claude.json":              60,
		"home/.gitconfig":                70,
		"home/.ssh/id_ed25519":           80,
		"home/.codex/sessions/s2.json":   5,
		"home/.config/gh/hosts.yml":      15,
		"home/.qoder/mcp.json":           25,
		"home/.claude/projects/p.json":   8,
	}
	cache := map[string]int{
		"home/.cache/go-build/abc": 200,
		"home/go/pkg/mod/m.zip":    300,
		"home/go/bin/tool":         35,
		"home/.npm/registry.tgz":   400,
		"home/.npm/_npx/pkg/i.js":  50,
	}
	unknown := map[string]int{
		"home/random.txt":        500,
		"home/.local/share/x.db": 600,
		"stray.txt":              700,
	}
	sum := func(files map[string]int) int64 {
		var total int64
		for rel, size := range files {
			writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), size)
			total += int64(size)
		}
		return total
	}
	wantProtected := sum(protected)
	wantCache := sum(cache)
	wantUnknown := sum(unknown)
	// Symlinks are never followed or counted, even inside classified trees.
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "payload"), 9999)
	if err := os.Symlink(filepath.Join(outside, "payload"), filepath.Join(root, "home", ".acpx", "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "home", ".cache", "linkdir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	usage, err := MeasureRuntimeHome(root)
	if err != nil {
		t.Fatalf("MeasureRuntimeHome: %v", err)
	}
	if usage.ProtectedBytes != wantProtected {
		t.Fatalf("protected = %d, want %d", usage.ProtectedBytes, wantProtected)
	}
	if usage.CacheBytes != wantCache {
		t.Fatalf("cache = %d, want %d", usage.CacheBytes, wantCache)
	}
	if usage.UnknownBytes != wantUnknown {
		t.Fatalf("unknown = %d, want %d", usage.UnknownBytes, wantUnknown)
	}
}

func TestMeasureRuntimeHomeMissingRoot(t *testing.T) {
	if _, err := MeasureRuntimeHome(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("missing root must return an error")
	}
}

func TestMeasureJobScratch(t *testing.T) {
	root := testRoot(t)
	if total, err := MeasureJobScratch(root); err != nil || total != 0 {
		t.Fatalf("missing scratch base = %d, %v; want 0, nil", total, err)
	}
	writeFile(t, filepath.Join(root, JobScratchDirName, "job-aaaaaaaaaaaaaaaa", "tmp", "a"), 100)
	writeFile(t, filepath.Join(root, JobScratchDirName, "job-bbbbbbbbbbbbbbbb", "go-tmp", "b"), 50)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "payload"), 9999)
	linkDir := filepath.Join(root, JobScratchDirName, "job-bbbbbbbbbbbbbbbb", "tmp")
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "payload"), filepath.Join(linkDir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	total, err := MeasureJobScratch(root)
	if err != nil {
		t.Fatalf("MeasureJobScratch: %v", err)
	}
	if total != 150 {
		t.Fatalf("scratch bytes = %d, want 150", total)
	}
}
