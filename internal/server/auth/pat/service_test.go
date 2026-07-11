package pat

import (
	"slices"
	"testing"
)

func TestValidateScopesIncludesEvidenceWriter(t *testing.T) {
	scopes, err := validateScopes([]string{"evidence:write", "issues:read", "evidence:write"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"evidence:write", "issues:read"}
	if !slices.Equal(scopes, want) {
		t.Fatalf("scopes = %v, want %v", scopes, want)
	}
}
