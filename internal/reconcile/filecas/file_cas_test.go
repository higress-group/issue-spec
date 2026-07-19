package filecas

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyFileMutationsPreflightsAllTargetsBeforeWrite(t *testing.T) {
	root := t.TempDir()
	mustWriteCASFile(t, root, "issue-spec/specs/a/spec.md", "a-before")
	mustWriteCASFile(t, root, "issue-spec/specs/b/spec.md", "b-drift")
	mutations := []FileMutation{
		fileMutationForTest("a", "issue-spec/specs/a/spec.md", "a-before", "a-after"),
		fileMutationForTest("b", "issue-spec/specs/b/spec.md", "b-before", "b-after"),
	}
	result, err := ApplyFileMutations(root, mutations)
	if err == nil || result.OK || result.Conflicted != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := mustReadCASFile(t, root, mutations[0].Path); got != "a-before" {
		t.Fatalf("preflight drift allowed an earlier write: %q", got)
	}
}

func TestApplyFileMutationsRecognizesCompleteAndPartialRetry(t *testing.T) {
	root := t.TempDir()
	mustWriteCASFile(t, root, "issue-spec/specs/a/spec.md", "a-after")
	mustWriteCASFile(t, root, "issue-spec/specs/b/spec.md", "b-before")
	mutations := []FileMutation{
		fileMutationForTest("b", "issue-spec/specs/b/spec.md", "b-before", "b-after"),
		fileMutationForTest("a", "issue-spec/specs/a/spec.md", "a-before", "a-after"),
	}
	result, err := ApplyFileMutations(root, mutations)
	if err != nil || !result.OK || result.Updated != 1 || result.Unchanged != 1 {
		t.Fatalf("partial retry result=%+v err=%v", result, err)
	}
	result, err = ApplyFileMutations(root, mutations)
	if err != nil || !result.OK || result.Updated != 0 || result.Unchanged != 2 {
		t.Fatalf("complete retry result=%+v err=%v", result, err)
	}
}

func TestApplyFileMutationsCreatesNewTargetAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	post := ImageForContent([]byte("new"))
	mutation := FileMutation{ID: "new", Path: "issue-spec/specs/new/spec.md", Preimage: MissingFileImage(), Postimage: post}
	if result, err := ApplyFileMutations(root, []FileMutation{mutation}); err != nil || !result.OK || result.Updated != 1 {
		t.Fatalf("new result=%+v err=%v", result, err)
	}

	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(root, "openspec")); err != nil {
		t.Fatal(err)
	}
	mutation = FileMutation{ID: "escape", Path: "openspec/specs/x/spec.md", Preimage: MissingFileImage(), Postimage: post}
	if _, err := ApplyFileMutations(root, []FileMutation{mutation}); err == nil {
		t.Fatal("accepted symlink target")
	}
}

func fileMutationForTest(id, path, before, after string) FileMutation {
	pre := ImageForContent([]byte(before))
	pre.Content = ""
	return FileMutation{ID: id, Path: path, Preimage: pre, Postimage: ImageForContent([]byte(after))}
}

func mustWriteCASFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadCASFile(t *testing.T, root, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
