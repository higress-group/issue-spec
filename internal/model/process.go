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

// ProcessWorkspaceManagement declares whether a PROCESS uses the portable
// workspace lifecycle or is independently allocated. Independent mode is
// deliberately explicit so missing legacy metadata keeps the safer managed
// behavior at final verification.
type ProcessWorkspaceManagement string

const (
	ProcessWorkspaceManaged     ProcessWorkspaceManagement = "managed"
	ProcessWorkspaceIndependent ProcessWorkspaceManagement = "independent"
)

var processWorkspaceManagementModes = []ProcessWorkspaceManagement{
	ProcessWorkspaceManaged,
	ProcessWorkspaceIndependent,
}

func (m ProcessWorkspaceManagement) Valid() bool {
	for _, candidate := range processWorkspaceManagementModes {
		if m == candidate {
			return true
		}
	}
	return false
}

func ParseProcessWorkspaceManagementValue(value string) (ProcessWorkspaceManagement, error) {
	mode := ProcessWorkspaceManagement(strings.ToLower(strings.TrimSpace(value)))
	if !mode.Valid() {
		return "", fmt.Errorf("unknown PROCESS workspace management %q (want %s)", value, processWorkspaceManagementList())
	}
	return mode, nil
}

type ProcessWorkspaceManagementResult struct {
	Management  ProcessWorkspaceManagement `json:"management"`
	Explicit    bool                       `json:"explicit"`
	Diagnostics []CanonicalDiagnostic      `json:"diagnostics,omitempty"`
}

func (r ProcessWorkspaceManagementResult) Blocking() bool {
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Severity == "error" {
			return true
		}
	}
	return false
}

// ParseProcessWorkspaceManagement reads the logical ### Workspace Management
// section. Missing declarations remain compatible with historical PROCESS
// comments and intentionally project to managed behavior at the gate.
func ParseProcessWorkspaceManagement(id, url, body string) ProcessWorkspaceManagementResult {
	values, headings := processWorkspaceManagementSectionValues(LogicalBody(body))
	result := ProcessWorkspaceManagementResult{Explicit: headings > 0}
	diagnostic := func(severity, element, message string) CanonicalDiagnostic {
		return CanonicalDiagnostic{Severity: severity, Type: "PROCESS", ID: id, URL: url, Element: element, Message: message}
	}
	if headings == 0 {
		result.Management = ProcessWorkspaceManaged
		return result
	}
	if headings != 1 {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-management-duplicate",
			"PROCESS has multiple `### Workspace Management` sections")}
		return result
	}
	if len(values) != 1 {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-management-invalid",
			"`### Workspace Management` must contain exactly one mode value")}
		return result
	}
	management, err := ParseProcessWorkspaceManagementValue(values[0])
	if err != nil {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-management-unknown", err.Error())}
		return result
	}
	result.Management = management
	return result
}

func processWorkspaceManagementSectionValues(logical string) ([]string, int) {
	lines := strings.Split(logical, "\n")
	var values []string
	headings := 0
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "### Workspace Management" {
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

func processWorkspaceManagementList() string {
	values := make([]string, 0, len(processWorkspaceManagementModes))
	for _, mode := range processWorkspaceManagementModes {
		values = append(values, string(mode))
	}
	return strings.Join(values, ", ")
}
