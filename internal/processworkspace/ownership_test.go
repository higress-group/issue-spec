package processworkspace

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeManagedOwnershipGrammar(t *testing.T) {
	got, err := NormalizeManagedOwnership([]string{"internal/model/**", "README.md", "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "internal/model/**"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized=%v want=%v", got, want)
	}
	for _, invalid := range []string{"", "/etc/passwd", `C:/private`, `C:\\private`, "~/private", ".", "..", "./a", "a/../b", "a//b", "a/*", "a/?", "a/[bc]", "a/**/b"} {
		if _, err := NormalizeManagedOwnership([]string{invalid}); err == nil {
			t.Errorf("unsafe ownership %q accepted", invalid)
		}
	}
}

func TestManagedOwnershipCollisionMatrix(t *testing.T) {
	tests := []struct {
		left, right []string
		want        bool
	}{
		{[]string{"a.txt"}, []string{"a.txt"}, true},
		{[]string{"internal/**"}, []string{"internal/x.go"}, true},
		{[]string{"internal/a/**"}, []string{"internal/b/**"}, false},
		{[]string{"go.mod"}, []string{"go.sum"}, false},
	}
	for _, test := range tests {
		got, err := ManagedOwnershipOverlaps(test.left, test.right)
		if err != nil || got != test.want {
			t.Fatalf("overlap(%v,%v)=%v err=%v want=%v", test.left, test.right, got, err, test.want)
		}
	}
}

func TestValidateManagedOwnershipRejectsLegacyQuestionAndBracketGlobs(t *testing.T) {
	for _, ownership := range [][]string{{"internal/?.go"}, {"internal/[ab].go"}} {
		if err := ValidateManagedOwnership(ownership, nil); err == nil {
			t.Fatalf("legacy ownership grammar accepted %v", ownership)
		}
		if _, err := ManagedOwnershipOverlaps(ownership, []string{"internal/a.go"}); err == nil {
			t.Fatalf("overlap accepted legacy ownership grammar %v", ownership)
		}
	}
	if err := ValidateManagedOwnership([]string{"internal/**"}, []string{"go.mod"}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedTouchpointDoesNotGrantWriteScope(t *testing.T) {
	err := ValidateManagedWriteScope([]string{"internal/**"}, []string{"go.mod"}, []string{"internal/x.go", "go.mod"})
	if !errors.Is(err, ErrOwnershipViolation) {
		t.Fatalf("shared touchpoint granted write permission: %v", err)
	}
	if err := ValidateManagedWriteScope([]string{"internal/**", "go.mod"}, []string{"go.mod"}, []string{"internal/x.go", "go.mod"}); err != nil {
		t.Fatal(err)
	}
}
