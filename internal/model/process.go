package model

import (
	"fmt"
	"strings"
)

// ProcessExecutionClass identifies the evidence responsibility of a PROCESS.
// The zero value is not valid: generators default it explicitly, while readers
// project a missing legacy section to the conservative change-bearing class.
type ProcessExecutionClass string

const (
	ProcessExecutionChangeBearing ProcessExecutionClass = "change-bearing"
	ProcessExecutionVerification  ProcessExecutionClass = "verification"
	ProcessExecutionReview        ProcessExecutionClass = "review"
	ProcessExecutionOrchestration ProcessExecutionClass = "orchestration"
	ProcessExecutionExternal      ProcessExecutionClass = "external"
)

var processExecutionClasses = []ProcessExecutionClass{
	ProcessExecutionChangeBearing,
	ProcessExecutionVerification,
	ProcessExecutionReview,
	ProcessExecutionOrchestration,
	ProcessExecutionExternal,
}

func (c ProcessExecutionClass) Valid() bool {
	for _, candidate := range processExecutionClasses {
		if c == candidate {
			return true
		}
	}
	return false
}

// ParseProcessExecutionClassValue validates a structured generator value.
func ParseProcessExecutionClassValue(value string) (ProcessExecutionClass, error) {
	class := ProcessExecutionClass(strings.ToLower(strings.TrimSpace(value)))
	if !class.Valid() {
		return "", fmt.Errorf("unknown PROCESS execution class %q (want %s)", value, processExecutionClassList())
	}
	return class, nil
}

// ProcessExecutionClassResult is the compatibility projection consumed by
// evidence policy. Explicit is false only for a legacy body with no section.
// Such a body remains readable and safely projects to change-bearing while the
// warning provides a migration signal. Error diagnostics are fail-closed.
type ProcessExecutionClassResult struct {
	Class       ProcessExecutionClass `json:"class"`
	Explicit    bool                  `json:"explicit"`
	Diagnostics []CanonicalDiagnostic `json:"diagnostics,omitempty"`
}

func (r ProcessExecutionClassResult) Blocking() bool {
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Severity == "error" {
			return true
		}
	}
	return false
}

// ParseProcessExecutionClass reads the logical ### Execution Class section.
// Missing legacy metadata is deliberately not a canonical error: historical
// PROCESS bodies remain valid, but downstream policy sees change-bearing plus a
// migration warning. Empty, duplicate, multi-value, and unknown sections block.
func ParseProcessExecutionClass(id, url, body string) ProcessExecutionClassResult {
	values, headings := processExecutionClassSectionValues(LogicalBody(body))
	result := ProcessExecutionClassResult{Explicit: headings > 0}
	diagnostic := func(severity, element, message string) CanonicalDiagnostic {
		return CanonicalDiagnostic{Severity: severity, Type: "PROCESS", ID: id, URL: url, Element: element, Message: message}
	}
	if headings == 0 {
		result.Class = ProcessExecutionChangeBearing
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("warning", "execution-class-missing",
			"legacy PROCESS is missing `### Execution Class`; treating it as `change-bearing` until regenerated")}
		return result
	}
	if headings != 1 {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "execution-class-duplicate",
			"PROCESS has multiple `### Execution Class` sections")}
		return result
	}
	if len(values) != 1 {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "execution-class-invalid",
			"`### Execution Class` must contain exactly one class value")}
		return result
	}
	class, err := ParseProcessExecutionClassValue(values[0])
	if err != nil {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "execution-class-unknown", err.Error())}
		return result
	}
	result.Class = class
	return result
}

func processExecutionClassSectionValues(logical string) ([]string, int) {
	lines := strings.Split(logical, "\n")
	var values []string
	headings := 0
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "### Execution Class" {
			headings++
			inSection = true
			continue
		}
		if strings.HasPrefix(trimmed, "### ") {
			inSection = false
			continue
		}
		if !inSection || trimmed == "" {
			continue
		}
		values = append(values, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
	}
	return values, headings
}

func processExecutionClassList() string {
	values := make([]string, 0, len(processExecutionClasses))
	for _, class := range processExecutionClasses {
		values = append(values, string(class))
	}
	return strings.Join(values, ", ")
}
