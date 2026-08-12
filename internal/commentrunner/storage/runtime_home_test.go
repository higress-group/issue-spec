package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testScope() RuntimeScope {
	return RuntimeScope{Hostname: "host-1", Realm: "", Repo: "o/r", Runner: "runner-1"}
}

func TestRuntimeScopeHashDeterministicShape(t *testing.T) {
	first, err := RuntimeScopeHash(testScope())
	if err != nil {
		t.Fatalf("RuntimeScopeHash: %v", err)
	}
	second, err := RuntimeScopeHash(testScope())
	if err != nil {
		t.Fatalf("RuntimeScopeHash: %v", err)
	}
	if first != second {
		t.Fatalf("hash not deterministic: %q vs %q", first, second)
	}
	if !ValidHashName(first) {
		t.Fatalf("hash %q does not have the physical hash shape", first)
	}
}

func TestRuntimeScopeHashVariesPerField(t *testing.T) {
	base, err := RuntimeScopeHash(testScope())
	if err != nil {
		t.Fatalf("RuntimeScopeHash: %v", err)
	}
	variants := map[string]RuntimeScope{
		"hostname": {Hostname: "host-2", Repo: "o/r", Runner: "runner-1"},
		"realm":    {Hostname: "host-1", Realm: "enterprise", Repo: "o/r", Runner: "runner-1"},
		"repo":     {Hostname: "host-1", Repo: "o/r2", Runner: "runner-1"},
		"runner":   {Hostname: "host-1", Repo: "o/r", Runner: "runner-2"},
	}
	for field, scope := range variants {
		hash, err := RuntimeScopeHash(scope)
		if err != nil {
			t.Fatalf("RuntimeScopeHash %s: %v", field, err)
		}
		if hash == base {
			t.Fatalf("changing %s must change the scope hash", field)
		}
	}
}

func TestRuntimeScopeValidate(t *testing.T) {
	cases := map[string]RuntimeScope{
		"valid":              testScope(),
		"valid with realm":   {Hostname: "h", Realm: "ent", Repo: "o/r", Runner: "r"},
		"missing hostname":   {Repo: "o/r", Runner: "r"},
		"missing repo":       {Hostname: "h", Runner: "r"},
		"repo not canonical": {Hostname: "h", Repo: "repo-only", Runner: "r"},
		"missing runner":     {Hostname: "h", Repo: "o/r"},
		"whitespace only":    {Hostname: " ", Repo: "o/r", Runner: "r"},
	}
	for name, scope := range cases {
		err := scope.Validate()
		switch {
		case strings.HasPrefix(name, "valid") && err != nil:
			t.Fatalf("%s: unexpected error %v", name, err)
		case !strings.HasPrefix(name, "valid") && err == nil:
			t.Fatalf("%s: expected validation error", name)
		}
	}
}

func TestRuntimeHomeRootLayout(t *testing.T) {
	root := testRoot(t)
	scope := testScope()
	homeRoot, err := RuntimeHomeRoot(root, scope)
	if err != nil {
		t.Fatalf("RuntimeHomeRoot: %v", err)
	}
	if filepath.Dir(homeRoot) != filepath.Join(root, RunnerHomesDirName) {
		t.Fatalf("runtime home root %q is not below %s", homeRoot, RunnerHomesDirName)
	}
	if !ValidHashName(filepath.Base(homeRoot)) {
		t.Fatalf("runtime home base %q is not a scope hash", filepath.Base(homeRoot))
	}
}

func TestPrepareRuntimeHomeCreatesPrivateTree(t *testing.T) {
	root := testRoot(t)
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	for _, dir := range []string{paths.Root, paths.Home, paths.GHConfigDir, paths.XDGConfigHome, paths.CodexHome, paths.AcpxRuntimeDir} {
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatalf("lstat %q: %v", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			t.Fatalf("%q is not a non-symlink directory", dir)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%q perm = %o, want 0700", dir, info.Mode().Perm())
		}
	}
	data, err := os.ReadFile(filepath.Join(paths.Root, runtimeScopeFileName))
	if err != nil {
		t.Fatalf("read scope file: %v", err)
	}
	var binding runtimeScopeFile
	if err := json.Unmarshal(data, &binding); err != nil {
		t.Fatalf("parse scope file: %v", err)
	}
	if binding.SchemaVersion != runtimeScopeVersion || binding.Hostname != scope.Hostname ||
		binding.Realm != scope.Realm || binding.Repo != scope.Repo || binding.Runner != scope.Runner {
		t.Fatalf("scope binding = %+v, want scope %+v", binding, scope)
	}
	if binding.CreatedAt.IsZero() {
		t.Fatalf("scope binding created_at must be set")
	}
	// Idempotent: a second prepare validates and preserves the original binding.
	if _, err := PrepareRuntimeHome(root, scope); err != nil {
		t.Fatalf("second PrepareRuntimeHome: %v", err)
	}
	again, err := os.ReadFile(filepath.Join(paths.Root, runtimeScopeFileName))
	if err != nil {
		t.Fatalf("re-read scope file: %v", err)
	}
	if string(again) != string(data) {
		t.Fatalf("second prepare rewrote scope.json")
	}
}

func TestPrepareRuntimeHomeScopeMismatchFails(t *testing.T) {
	root := testRoot(t)
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	// Simulate a foreign binding landing at this scope hash (collision/tamper).
	foreign := runtimeScopeFile{
		SchemaVersion: runtimeScopeVersion,
		Hostname:      scope.Hostname,
		Repo:          "other/repo",
		Runner:        scope.Runner,
	}
	payload, err := json.Marshal(foreign)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.Root, runtimeScopeFileName), payload, 0o600); err != nil {
		t.Fatalf("write foreign scope: %v", err)
	}
	_, err = PrepareRuntimeHome(root, scope)
	if err == nil {
		t.Fatalf("scope mismatch must fail closed")
	}
	if !strings.Contains(err.Error(), "other/repo") || !strings.Contains(err.Error(), "o/r") {
		t.Fatalf("mismatch error must name both scopes, got: %v", err)
	}
}

func TestPrepareRuntimeHomeRejectsSymlink(t *testing.T) {
	root := testRoot(t)
	scope := testScope()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, RunnerHomesDirName)); err != nil {
		t.Fatalf("symlink base: %v", err)
	}
	if _, err := PrepareRuntimeHome(root, scope); err == nil {
		t.Fatalf("symlinked %s must fail closed", RunnerHomesDirName)
	}
}

func TestPrepareRuntimeHomeRejectsSymlinkedScopeDir(t *testing.T) {
	root := testRoot(t)
	scope := testScope()
	hash, err := RuntimeScopeHash(scope)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	base := filepath.Join(root, RunnerHomesDirName)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, hash)); err != nil {
		t.Fatalf("symlink scope dir: %v", err)
	}
	if _, err := PrepareRuntimeHome(root, scope); err == nil {
		t.Fatalf("symlinked scope directory must fail closed")
	}
}

func TestPrepareRuntimeHomeRejectsFileCollision(t *testing.T) {
	root := testRoot(t)
	if err := os.WriteFile(filepath.Join(root, RunnerHomesDirName), []byte("x"), 0o600); err != nil {
		t.Fatalf("write colliding file: %v", err)
	}
	if _, err := PrepareRuntimeHome(root, testScope()); err == nil {
		t.Fatalf("an existing file at %s must fail closed", RunnerHomesDirName)
	}
}

func TestJobScratchRootValidation(t *testing.T) {
	root := testRoot(t)
	valid := "job-0123456789abcdef"
	path, err := JobScratchRoot(root, valid)
	if err != nil {
		t.Fatalf("JobScratchRoot: %v", err)
	}
	if path != filepath.Join(root, JobScratchDirName, valid) {
		t.Fatalf("scratch root %q unexpected", path)
	}
	invalid := []string{
		"",
		"job-1",
		"job-0123456789abcde",   // too short
		"job-0123456789abcdef0", // too long
		"job-0123456789abcdeg",  // non-hex
		"JOB-0123456789abcdef",  // wrong prefix case
		" job-0123456789abcdef", // surrounding space
		"job-0123456789abcdef/extra",
	}
	for _, id := range invalid {
		if _, err := JobScratchRoot(root, id); err == nil {
			t.Fatalf("job id %q must be rejected", id)
		}
		if _, err := PrepareJobScratch(root, id); err == nil {
			t.Fatalf("PrepareJobScratch job id %q must be rejected", id)
		}
	}
}

func TestPrepareJobScratchLayout(t *testing.T) {
	root := testRoot(t)
	jobID := "job-aaaaaaaaaaaaaaaa"
	paths, err := PrepareJobScratch(root, jobID)
	if err != nil {
		t.Fatalf("PrepareJobScratch: %v", err)
	}
	for _, dir := range []string{paths.Root, paths.Tmp, paths.GoTmp, paths.XDGData, paths.XDGState} {
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatalf("lstat %q: %v", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%q must be a 0700 non-symlink directory, mode=%v", dir, info.Mode())
		}
	}
	// Idempotent re-prepare.
	if _, err := PrepareJobScratch(root, jobID); err != nil {
		t.Fatalf("second PrepareJobScratch: %v", err)
	}
}

func TestPrepareJobScratchRejectsSymlink(t *testing.T) {
	root := testRoot(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, JobScratchDirName)); err != nil {
		t.Fatalf("symlink base: %v", err)
	}
	if _, err := PrepareJobScratch(root, "job-bbbbbbbbbbbbbbbb"); err == nil {
		t.Fatalf("symlinked %s must fail closed", JobScratchDirName)
	}
}
