package model

import (
	"strings"
	"testing"
)

func TestParseProcessExecutionClassMatrix(t *testing.T) {
	for _, class := range processExecutionClasses {
		t.Run(string(class), func(t *testing.T) {
			body := "## Process: p\n\n### Execution Class\n\n- " + string(class) + "\n\n### Parent TASK\n\n- TASK-001"
			result := ParseProcessExecutionClass("PROCESS-001", "https://example/process", body)
			if result.Class != class || !result.Explicit || result.Blocking() || len(result.Diagnostics) != 0 {
				t.Fatalf("unexpected parse result: %+v", result)
			}
		})
	}
}

func TestParseProcessExecutionClassLegacyDefaultsSafely(t *testing.T) {
	body := "## Process: legacy\n\n### Parent TASK\n\n- TASK-001"
	result := ParseProcessExecutionClass("PROCESS-OLD", "https://example/old", body)
	if result.Class != ProcessExecutionChangeBearing || result.Explicit || result.Blocking() {
		t.Fatalf("legacy PROCESS must conservatively project to change-bearing: %+v", result)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Severity != "warning" ||
		result.Diagnostics[0].Element != "execution-class-missing" || result.Diagnostics[0].ID != "PROCESS-OLD" {
		t.Fatalf("legacy migration diagnostic missing context: %+v", result.Diagnostics)
	}
	// Migration warnings do not make historical PROCESS artifacts noncanonical.
	if diags := ValidateCanonicalBody("PROCESS", "PROCESS-OLD", "", body); len(diags) != 0 {
		t.Fatalf("legacy PROCESS should remain canonical: %+v", diags)
	}
}

func TestParseProcessExecutionClassRejectsUnsafeMetadata(t *testing.T) {
	cases := []struct {
		name    string
		section string
		element string
	}{
		{"unknown", "### Execution Class\n\n- deploy", "execution-class-unknown"},
		{"empty", "### Execution Class\n\n", "execution-class-invalid"},
		{"multiple values", "### Execution Class\n\n- review\n- orchestration", "execution-class-invalid"},
		{"duplicate sections", "### Execution Class\n\n- review\n\n### Execution Class\n\n- verification", "execution-class-duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "## Process: p\n\n### Parent TASK\n\n- TASK-001\n\n" + tc.section
			result := ParseProcessExecutionClass("PROCESS-001", "", body)
			if !result.Blocking() || len(result.Diagnostics) != 1 || result.Diagnostics[0].Element != tc.element {
				t.Fatalf("unexpected parse result: %+v", result)
			}
			diags := ValidateCanonicalBody("PROCESS", "PROCESS-001", "", body)
			if len(diags) != 1 || diags[0].Element != tc.element {
				t.Fatalf("unsafe metadata must block canonical validation: %+v", diags)
			}
		})
	}
}

func TestParseProcessExecutionClassValue(t *testing.T) {
	class, err := ParseProcessExecutionClassValue(" Review ")
	if err != nil || class != ProcessExecutionReview {
		t.Fatalf("expected normalized review class, got %q err=%v", class, err)
	}
	if _, err := ParseProcessExecutionClassValue("release"); err == nil || !strings.Contains(err.Error(), "change-bearing") {
		t.Fatalf("unknown class should report accepted values: %v", err)
	}
}
