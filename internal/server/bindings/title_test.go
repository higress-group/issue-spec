package bindings

import (
	"encoding/json"
	"testing"
)

func TestReferenceEqualIgnoresTranslationTitleSuffix(t *testing.T) {
	metadata := json.RawMessage(`{"source":"github"}`)
	base := Reference{CanonicalURL: "https://github.com/o/r/issues/1", Title: ptr("修复导出问题"), LifecycleState: "open", Visibility: "public", Metadata: metadata}
	input := UpsertReferenceInput{CanonicalURL: "https://github.com/o/r/issues/1", Title: ptr("修复导出问题 || Fix the export issue"), LifecycleState: "open", Visibility: "public", Metadata: metadata}
	if !referenceEqual(base, input) {
		t.Fatal("translation title suffix must not look like drift")
	}
	reversed := Reference{CanonicalURL: "https://github.com/o/r/issues/1", Title: ptr("修复导出问题 || Fix the export issue"), LifecycleState: "open", Visibility: "public", Metadata: metadata}
	if !referenceEqual(reversed, UpsertReferenceInput{CanonicalURL: "https://github.com/o/r/issues/1", Title: ptr("修复导出问题"), LifecycleState: "open", Visibility: "public", Metadata: metadata}) {
		t.Fatal("suffix on either side must compare equal")
	}
	if referenceEqual(base, UpsertReferenceInput{CanonicalURL: "https://github.com/o/r/issues/1", Title: ptr("完全不同的标题"), LifecycleState: "open", Visibility: "public", Metadata: metadata}) {
		t.Fatal("genuinely different title must compare unequal")
	}
}

func TestReferenceEqualKeepsOtherFieldDriftDetection(t *testing.T) {
	metadata := json.RawMessage(`{"source":"github"}`)
	base := Reference{CanonicalURL: "https://github.com/o/r/issues/1", Title: ptr("t"), LifecycleState: "open", Visibility: "public", Metadata: metadata}
	drifted := UpsertReferenceInput{CanonicalURL: "https://github.com/o/r/issues/1", Title: ptr("t"), LifecycleState: "closed", Visibility: "public", Metadata: metadata}
	if referenceEqual(base, drifted) {
		t.Fatal("lifecycle drift must compare unequal")
	}
}

func ptr(value string) *string { return &value }
