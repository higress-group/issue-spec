package storage

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// TestSharedRuntimeHomeReusesGoCachesAcrossJobs is the real-toolchain
// acceptance proof for the runner-scoped shared layout: two jobs sharing one
// runtime HOME but receiving distinct disposable scratch directories reuse the
// HOME-anchored Go build and module caches, including under concurrency.
// Skipped in -short mode and when no go toolchain is on PATH.
func TestSharedRuntimeHomeReusesGoCachesAcrossJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("acceptance test drives a real go toolchain")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	root := t.TempDir()
	scope := RuntimeScope{Hostname: "github.com", Repo: "o/r", Runner: "acceptance"}
	home, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("prepare runtime home: %v", err)
	}
	scratchA, err := PrepareJobScratch(root, "job-aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("prepare job A scratch: %v", err)
	}
	scratchB, err := PrepareJobScratch(root, "job-bbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("prepare job B scratch: %v", err)
	}

	// A tiny module with exactly one external dependency, served by a local
	// file proxy so the acceptance never touches the network.
	proxyDir := t.TempDir()
	writeFileProxyModule(t, proxyDir, "example.com/dep", "v1.0.0")
	moduleDir := t.TempDir()
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), "module example.com/acceptance\n\ngo 1.21\n\nrequire example.com/dep v1.0.0\n")
	writeTestFile(t, filepath.Join(moduleDir, "main.go"), `package main

import (
	"fmt"

	"example.com/dep"
)

func main() { fmt.Println(dep.Greeting()) }
`)

	runGo := func(scratch JobScratchPaths, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + home.Home,
			"TMPDIR=" + scratch.Tmp,
			"GOTMPDIR=" + scratch.GoTmp,
			"XDG_DATA_HOME=" + scratch.XDGData,
			"XDG_STATE_HOME=" + scratch.XDGState,
			"GOPROXY=file://" + proxyDir,
			"GOSUMDB=off",
			"GOFLAGS=-mod=mod",
			"CGO_ENABLED=0",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	// Job A warms the shared caches.
	runGo(scratchA, moduleDir, "mod", "download")
	binA := filepath.Join(t.TempDir(), "bin-a")
	runGo(scratchA, moduleDir, "build", "-buildvcs=false", "-o", binA, ".")

	gocache := strings.TrimSpace(runGo(scratchA, moduleDir, "env", "GOCACHE"))
	gomodcache := strings.TrimSpace(runGo(scratchA, moduleDir, "env", "GOMODCACHE"))
	if gocache == "" || gomodcache == "" {
		t.Fatalf("go env caches must be set: GOCACHE=%q GOMODCACHE=%q", gocache, gomodcache)
	}
	for name, dir := range map[string]string{"GOCACHE": gocache, "GOMODCACHE": gomodcache} {
		if rel, err := filepath.Rel(home.Home, dir); err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("%s=%q must anchor below the shared home %q", name, dir, home.Home)
		}
	}
	if count := countRegularFiles(t, gocache); count == 0 {
		t.Fatalf("shared GOCACHE %q is empty after job A build", gocache)
	}
	if _, err := os.Lstat(filepath.Join(gomodcache, "example.com", "dep@v1.0.0")); err != nil {
		t.Fatalf("shared GOMODCACHE %q missing the downloaded module: %v", gomodcache, err)
	}
	// Job A scratch held only disposable temp content; the caches live in the
	// shared home, not the scratch.
	if count := countRegularFiles(t, scratchA.GoTmp); count != 0 {
		t.Fatalf("job GOTMPDIR must not retain build content after the build: %d files", count)
	}

	// Job B: same shared HOME, distinct scratch. The caches resolve to the
	// identical directories and the build succeeds without network access —
	// the module is already in the shared module cache (GOPROXY=off proves it).
	runGoOff := func(scratch JobScratchPaths, args ...string) string {
		t.Helper()
		cmd := exec.Command("go", args...)
		cmd.Dir = moduleDir
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + home.Home,
			"TMPDIR=" + scratch.Tmp,
			"GOTMPDIR=" + scratch.GoTmp,
			"XDG_DATA_HOME=" + scratch.XDGData,
			"XDG_STATE_HOME=" + scratch.XDGState,
			"GOPROXY=off",
			"GOFLAGS=-mod=mod",
			"CGO_ENABLED=0",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s (offline) failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	binB := filepath.Join(t.TempDir(), "bin-b")
	runGoOff(scratchB, "build", "-buildvcs=false", "-o", binB, ".")
	if got := strings.TrimSpace(runGoOff(scratchB, "env", "GOCACHE")); got != gocache {
		t.Fatalf("job B GOCACHE = %q, want the shared %q", got, gocache)
	}
	if got := strings.TrimSpace(runGoOff(scratchB, "env", "GOMODCACHE")); got != gomodcache {
		t.Fatalf("job B GOMODCACHE = %q, want the shared %q", got, gomodcache)
	}
	dataA, err := os.ReadFile(binA)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := os.ReadFile(binB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dataA, dataB) {
		t.Fatalf("jobs sharing one runtime home must reproduce identical build outputs")
	}

	// Concurrent pair: two goroutines, one shared HOME, distinct GOTMPDIR.
	scratchC, err := PrepareJobScratch(root, "job-cccccccccccccccc")
	if err != nil {
		t.Fatal(err)
	}
	scratchD, err := PrepareJobScratch(root, "job-dddddddddddddddd")
	if err != nil {
		t.Fatal(err)
	}
	bins := []string{filepath.Join(t.TempDir(), "bin-c"), filepath.Join(t.TempDir(), "bin-d")}
	scratches := []JobScratchPaths{scratchC, scratchD}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range bins {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bins[i], ".")
			cmd.Dir = moduleDir
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + home.Home,
				"TMPDIR=" + scratches[i].Tmp,
				"GOTMPDIR=" + scratches[i].GoTmp,
				"XDG_DATA_HOME=" + scratches[i].XDGData,
				"XDG_STATE_HOME=" + scratches[i].XDGState,
				"GOPROXY=off",
				"GOFLAGS=-mod=mod",
				"CGO_ENABLED=0",
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				errs[i] = fmt.Errorf("concurrent build: %w\n%s", err, out)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	dataC, err := os.ReadFile(bins[0])
	if err != nil {
		t.Fatal(err)
	}
	dataD, err := os.ReadFile(bins[1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dataC, dataD) || !bytes.Equal(dataA, dataC) {
		t.Fatalf("concurrent builds on one shared home must produce identical outputs")
	}
}

// writeFileProxyModule publishes a one-file module into a local GOPROXY
// directory layout so tests can exercise the module cache offline.
func writeFileProxyModule(t *testing.T, proxyDir, module, version string) {
	t.Helper()
	gomod := "module " + module + "\n\ngo 1.21\n"
	source := "package dep\n\n// Greeting identifies the dep build.\nfunc Greeting() string { return \"dep-ok\" }\n"
	atVersion := module + "@" + version
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		atVersion + "/go.mod": gomod,
		atVersion + "/dep.go": source,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(proxyDir, module, "@v")
	writeTestFile(t, filepath.Join(base, version+".info"), `{"Version":"`+version+`","Time":"2024-01-01T00:00:00Z"}`)
	writeTestFile(t, filepath.Join(base, version+".mod"), gomod)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, version+".zip"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func countRegularFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestPrepareRuntimeHomeAndScratchNeverTouchForeignRoots is the fast unit-level
// cutover acceptance: prepare, reconcile, and evict against a fresh root while
// a pre-existing old-style root (with a `.sessions/<hash>/home` fixture) sits
// elsewhere on disk, and prove every byte of the old root is untouched.
func TestPrepareRuntimeHomeAndScratchNeverTouchForeignRoots(t *testing.T) {
	newRoot := t.TempDir()
	oldRoot := t.TempDir()
	oldFixture := filepath.Join(oldRoot, ".sessions", "0123456789abcdef0123456789abcdef", "home")
	writeTestFile(t, filepath.Join(oldFixture, ".claude.json"), `{"old":"session"}`)
	writeTestFile(t, filepath.Join(oldRoot, ".storage", "marker"), "old sidecar")
	before := snapshotTree(t, oldRoot)

	scope := RuntimeScope{Hostname: "github.com", Repo: "o/r", Runner: "cutover"}
	home, err := PrepareRuntimeHome(newRoot, scope)
	if err != nil {
		t.Fatalf("prepare runtime home: %v", err)
	}
	if rel, relErr := filepath.Rel(newRoot, home.Root); relErr != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("runtime home %q must anchor below the new root %q", home.Root, newRoot)
	}
	scratch, err := PrepareJobScratch(newRoot, "job-eeeeeeeeeeeeeeee")
	if err != nil {
		t.Fatalf("prepare job scratch: %v", err)
	}
	writeTestFile(t, filepath.Join(scratch.Tmp, "leftover"), "stale")

	svc, err := NewService(ServiceConfig{
		WorkspaceRoot: newRoot,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return state.NewState(), nil },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()
	if err := svc.RecordRuntimeHome(context.Background(), scope, home); err != nil {
		t.Fatalf("record runtime home: %v", err)
	}
	if err := svc.RecordJobScratch(context.Background(), "o/r", "job-eeeeeeeeeeeeeeee", scratch.Root); err != nil {
		t.Fatalf("record job scratch: %v", err)
	}
	if _, err := svc.ReconcileStorage(context.Background(), true, true); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := svc.ReconcileJobScratch(context.Background(), true); err != nil {
		t.Fatalf("reconcile job scratch: %v", err)
	}
	if _, err := svc.EvictRuntimeCaches(context.Background(), true); err != nil {
		t.Fatalf("evict runtime caches: %v", err)
	}
	// The passes did real work on the new root: the stale scratch of the
	// unknown job is reclaimed and the shared home is kept.
	if _, err := os.Lstat(scratch.Root); !os.IsNotExist(err) {
		t.Fatalf("stale scratch must be reclaimed on the new root, err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(home.Root, "scope.json")); err != nil {
		t.Fatalf("shared runtime home must survive reconciliation: %v", err)
	}

	after := snapshotTree(t, oldRoot)
	if !equalStringMaps(before, after) {
		t.Fatalf("old-style root was read-for-import, modified, or deleted:\nbefore=%v\nafter=%v", before, after)
	}
}

// snapshotTree maps every file below root to its content so a byte-exact
// before/after comparison proves the tree was never touched.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out[rel] = string(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, ok := b[k]; !ok || other != v {
			return false
		}
	}
	return true
}
