package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerLockConflictsInProcess(t *testing.T) {
	root := testRoot(t)
	owner, err := AcquireOwner(root)
	if err != nil {
		t.Fatalf("AcquireOwner: %v", err)
	}
	if owner.Root() != root {
		t.Fatalf("owner root = %q, want %q", owner.Root(), root)
	}
	if _, err := AcquireOwner(root); !errors.Is(err, ErrOwnerLocked) {
		t.Fatalf("second acquire err = %v, want ErrOwnerLocked", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := AcquireOwner(root)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	defer again.Release()
}

func TestOwnerLockWritesHolderMetadata(t *testing.T) {
	root := testRoot(t)
	owner, err := AcquireOwner(root)
	if err != nil {
		t.Fatalf("AcquireOwner: %v", err)
	}
	defer owner.Release()
	data, err := os.ReadFile(filepath.Join(root, ".storage", "owner.lock"))
	if err != nil {
		t.Fatalf("read owner lock: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "pid=") || !strings.Contains(content, "root_identity="+wantIdentity(t, root)) {
		t.Fatalf("owner lock metadata incomplete: %q", content)
	}
}

func TestEnsureOwnerUsesContextHandle(t *testing.T) {
	root := testRoot(t)
	owner, err := AcquireOwner(root)
	if err != nil {
		t.Fatalf("AcquireOwner: %v", err)
	}
	defer owner.Release()
	ctx := WithOwner(context.Background(), owner)
	got, release, err := EnsureOwner(ctx, root)
	if err != nil {
		t.Fatalf("EnsureOwner: %v", err)
	}
	if got != owner {
		t.Fatalf("EnsureOwner returned a different handle")
	}
	release()
	// Context handle release must be a no-op: a fresh acquire still conflicts.
	if _, err := AcquireOwner(root); !errors.Is(err, ErrOwnerLocked) {
		t.Fatalf("after ctx release, acquire err = %v, want ErrOwnerLocked", err)
	}
}

func TestEnsureOwnerAcquiresWhenContextEmpty(t *testing.T) {
	root := testRoot(t)
	owner, release, err := EnsureOwner(context.Background(), root)
	if err != nil {
		t.Fatalf("EnsureOwner: %v", err)
	}
	if owner == nil {
		t.Fatalf("EnsureOwner returned nil owner")
	}
	if _, err := AcquireOwner(root); !errors.Is(err, ErrOwnerLocked) {
		t.Fatalf("concurrent acquire err = %v, want ErrOwnerLocked", err)
	}
	release()
	late, err := AcquireOwner(root)
	if err != nil {
		t.Fatalf("acquire after EnsureOwner release: %v", err)
	}
	defer late.Release()
}

func TestValidateInventoryEntry(t *testing.T) {
	root := testRoot(t)
	sessionsDir := filepath.Join(root, ".sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	valid := strings.Repeat("ab", 16)
	if err := os.Mkdir(filepath.Join(sessionsDir, valid), 0o700); err != nil {
		t.Fatalf("mkdir valid: %v", err)
	}
	path, err := ValidateInventoryEntry(root, ".sessions", valid)
	if err != nil {
		t.Fatalf("valid entry: %v", err)
	}
	if path != filepath.Join(sessionsDir, valid) {
		t.Fatalf("path = %q", path)
	}

	cases := map[string]string{
		"too short":         "ab",
		"too long":          strings.Repeat("ab", 17),
		"uppercase hex":     strings.Repeat("AB", 16),
		"non hex":           strings.Repeat("zz", 16),
		"dot":               ".",
		"dotdot":            "..",
		"separator":         "a/b",
		"protected storage": ".storage",
		"protected locks":   ".locks",
		"workspace like":    "ws-123",
	}
	for name, entry := range cases {
		if _, err := ValidateInventoryEntry(root, ".sessions", entry); err == nil {
			t.Fatalf("%s: entry %q must be rejected", name, entry)
		}
	}

	// Symlink entries are rejected without following.
	target := filepath.Join(root, "ws-real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	linkHash := strings.Repeat("cd", 16)
	if err := os.Symlink(target, filepath.Join(sessionsDir, linkHash)); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := ValidateInventoryEntry(root, ".sessions", linkHash); err == nil {
		t.Fatalf("symlink entry must be rejected")
	}

	// Regular files are rejected.
	fileHash := strings.Repeat("ef", 16)
	if err := os.WriteFile(filepath.Join(sessionsDir, fileHash), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := ValidateInventoryEntry(root, ".sessions", fileHash); err == nil {
		t.Fatalf("regular file entry must be rejected")
	}
}

func TestValidateDeletionTargetConfinesToRoot(t *testing.T) {
	root := testRoot(t)
	sessionsDir := filepath.Join(root, ".sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	hash := strings.Repeat("ab", 16)
	target := filepath.Join(sessionsDir, hash)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := ValidateDeletionTarget(root, target, hash); err != nil {
		t.Fatalf("valid target: %v", err)
	}
	for name, bad := range map[string]string{
		"root itself":     root,
		"sessions dir":    sessionsDir,
		"storage dir":     filepath.Join(root, ".storage"),
		"locks dir":       filepath.Join(root, ".locks"),
		"outside root":    filepath.Dir(root),
		"wrong hash name": filepath.Join(sessionsDir, strings.Repeat("cd", 16)),
	} {
		if err := ValidateDeletionTarget(root, bad, hash); err == nil {
			t.Fatalf("%s: target %q must be rejected", name, bad)
		}
	}
	// Identity change: replace the dir with a symlink before deletion.
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target: %v", err)
	}
	real := filepath.Join(root, "ws-real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.Symlink(real, target); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := ValidateDeletionTarget(root, target, hash); err == nil {
		t.Fatalf("symlink replacement must be rejected")
	}
}
