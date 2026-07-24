package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseGoldenFencesAndExactSelection(t *testing.T) {
	body := "before\r\n  ~~~~html-preview id=design-review version=1 title=\"设计 review\" height=900\r\n" +
		"<!doctype html>\r\n<p>exact</p>\r\n  ~~~~~   \r\nafter"
	result := ParseWithSource(body, "issue:7")
	if len(result.Descriptors) != 1 {
		t.Fatalf("descriptors = %+v", result.Descriptors)
	}
	descriptor := result.Descriptors[0]
	source := "<!doctype html>\r\n<p>exact</p>\r\n"
	sum := sha256.Sum256([]byte(source))
	if descriptor.ID != "design-review" || descriptor.Version != 1 || descriptor.Title != "设计 review" ||
		descriptor.Height != MaxHeight || descriptor.ByteSize != len(source) ||
		descriptor.Digest != hex.EncodeToString(sum[:]) || descriptor.SourceLocator != "issue:7" ||
		!descriptor.Omitted || !descriptor.Executable || len(descriptor.Diagnostics) != 0 {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	selected, err := result.Select("design-review")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Source != source {
		t.Fatalf("source = %q, want exact %q", selected.Source, source)
	}
	if body[descriptor.SourceRange.Start:descriptor.SourceRange.End] != source ||
		body[descriptor.Range.Start:descriptor.Range.End] != "  ~~~~html-preview id=design-review version=1 title=\"设计 review\" height=900\r\n"+source+"  ~~~~~   \r\n" {
		t.Fatal("reported byte ranges are not exact")
	}
}

func TestParseBackticksIndentationAndDefaults(t *testing.T) {
	body := "   ```html-preview id=a version=1 title=\"\"\nx\n   ```\n"
	descriptor := Parse(body).Descriptors[0]
	if descriptor.ID != "a" || descriptor.Height != DefaultHeight || !descriptor.Executable {
		t.Fatalf("descriptor = %+v", descriptor)
	}
}

func TestOrdinaryFenceContainsNoNestedPreview(t *testing.T) {
	body := "````markdown\n```html-preview id=hidden version=1\n/new escape\n```\n````\n"
	if descriptors := Parse(body).Descriptors; len(descriptors) != 0 {
		t.Fatalf("descriptors = %+v", descriptors)
	}
}

func TestSemanticViewMasksOnlyPreviewSourceAndPreservesBytes(t *testing.T) {
	body := "prefix\n```html-preview id=a version=1\n<!-- issue-spec:type=TASK id=TASK-999 version=1 -->\n/new hostile\n```\nsuffix"
	result := Parse(body)
	view := result.SemanticView()
	if len(view) != len(body) {
		t.Fatalf("semantic view length = %d, want %d", len(view), len(body))
	}
	if strings.Contains(view, "TASK-999") || strings.Contains(view, "/new hostile") {
		t.Fatalf("preview source leaked into semantic view: %q", view)
	}
	if !strings.Contains(view, "prefix\n```html-preview id=a version=1\n") || !strings.HasSuffix(view, "```\nsuffix") {
		t.Fatalf("surrounding markdown changed: %q", view)
	}
}

func TestMalformedUnknownDuplicateUnclosedAndOversizedFailClosed(t *testing.T) {
	oversized := strings.Repeat("x", MaxSourceSize+1)
	body := "```html-preview id=dup version=1 bad=value\none\n```\n" +
		"```html-preview id=dup version=2\ntwo\n```\n" +
		"```html-preview id=open version=1\n" + oversized
	result := Parse(body)
	if len(result.Descriptors) != 3 {
		t.Fatalf("descriptors = %d", len(result.Descriptors))
	}
	wantCodes := [][]string{
		{"malformed_metadata", "duplicate_id"},
		{"unknown_version", "duplicate_id"},
		{"unclosed_fence", "source_too_large"},
	}
	for i, descriptor := range result.Descriptors {
		if descriptor.Executable {
			t.Fatalf("descriptor %d unexpectedly executable: %+v", i, descriptor)
		}
		for _, code := range wantCodes[i] {
			if !hasDiagnostic(descriptor, code) {
				t.Fatalf("descriptor %d missing %s: %+v", i, code, descriptor)
			}
		}
		if descriptor.ID != "" {
			if _, err := result.Select(descriptor.ID); err == nil {
				t.Fatalf("selection of inert descriptor %q succeeded", descriptor.ID)
			}
		}
	}
}

func TestMetadataValidationAndUTF8TitleLimit(t *testing.T) {
	tests := []struct {
		name string
		info string
		code string
	}{
		{name: "missing id", info: "version=1", code: "missing_id"},
		{name: "bad id", info: "id=Bad version=1", code: "invalid_id"},
		{name: "missing version", info: "id=a", code: "missing_version"},
		{name: "bad version", info: "id=a version=no", code: "malformed_metadata"},
		{name: "duplicate metadata", info: "id=a id=b version=1", code: "malformed_metadata"},
		{name: "unclosed quote", info: "id=a version=1 title=\"bad", code: "malformed_metadata"},
		{name: "long title", info: "id=a version=1 title=\"" + strings.Repeat("界", 121) + "\"", code: "title_too_long"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := "```html-preview " + test.info + "\nx\n```\n"
			descriptor := Parse(body).Descriptors[0]
			if !hasDiagnostic(descriptor, test.code) || descriptor.Executable {
				t.Fatalf("descriptor = %+v", descriptor)
			}
		})
	}
}

func TestPreviewCountLimitMakesEveryBlockInert(t *testing.T) {
	var body strings.Builder
	for i := 0; i < MaxPreviews+1; i++ {
		body.WriteString("```html-preview id=p")
		body.WriteByte(byte('a' + i))
		body.WriteString(" version=1\nsource\n```\n")
	}
	result := Parse(body.String())
	if len(result.Descriptors) != MaxPreviews+1 {
		t.Fatalf("descriptor count = %d", len(result.Descriptors))
	}
	for _, descriptor := range result.Descriptors {
		if descriptor.Executable || !hasDiagnostic(descriptor, "preview_count_exceeded") {
			t.Fatalf("descriptor did not fail count limit: %+v", descriptor)
		}
	}
}

func TestSelectRejectsUnknownAndInvalidIDs(t *testing.T) {
	result := Parse("```html-preview id=known version=1\nok\n```\n")
	for _, id := range []string{"missing", "BAD"} {
		if _, err := result.Select(id); err == nil {
			t.Fatalf("Select(%q) succeeded", id)
		}
	}
}

func hasDiagnostic(descriptor Descriptor, code string) bool {
	for _, diagnostic := range descriptor.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
