package model

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/higress-group/issue-spec/internal/durable"
)

// CanonicalDiagnostic describes a single canonical-discipline problem found in a
// typed comment body. It carries enough context (type, id, url, element) for a
// fresh coordinator to locate and fix the artifact from GitHub state alone.
type CanonicalDiagnostic struct {
	Severity string `json:"severity"`
	Type     string `json:"type"`
	ID       string `json:"id"`
	URL      string `json:"url,omitempty"`
	Element  string `json:"element"`
	Message  string `json:"message"`
}

var normativeRe = regexp.MustCompile(`\b(MUST|SHALL)\b`)

// specElement pairs a machine-readable element key with an actionable message.
type specElement struct {
	element string
	message string
}

// LogicalBody returns the logical content of a typed comment body after
// stripping the typed marker comment and the visible metadata header
// (Agent/Type/ID/Status/Scope/Links block). Raw generated bodies and
// already-wrapped bodies therefore reduce to the same logical representation so
// every validation surface validates the same text.
func LogicalBody(body string) string {
	stripped := strings.TrimLeft(markerRe.ReplaceAllString(body, ""), "\n")
	lines := strings.Split(stripped, "\n")

	sawHeader := false
	inLinks := false
	end := 0
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			if sawHeader {
				end = i + 1
				break
			}
			continue
		}
		if inLinks {
			if strings.HasPrefix(trimmed, "- ") {
				end = i + 1
				continue
			}
			end = i
			break
		}
		key, _, ok := strings.Cut(trimmed, ":")
		if ok && isHeaderKey(strings.TrimSpace(key)) {
			sawHeader = true
			end = i + 1
			if strings.TrimSpace(key) == "Links" {
				inLinks = true
			}
			continue
		}
		// First non-header content line ends header stripping.
		if sawHeader {
			end = i
		}
		break
	}

	if !sawHeader {
		return strings.TrimSpace(stripped)
	}
	return strings.TrimSpace(strings.Join(lines[end:], "\n"))
}

func isHeaderKey(key string) bool {
	switch key {
	case "Agent", "Type", "ID", "Status", "Scope", "Links":
		return true
	default:
		return false
	}
}

// specCanonicalElements returns the canonical SPEC elements missing from an
// already-logical SPEC body, in a stable order.
func specCanonicalElements(logical string) []specElement {
	logical = strings.TrimSpace(logical)
	var missing []specElement
	if !strings.Contains(logical, "## Requirement:") {
		missing = append(missing, specElement{"requirement-heading", "missing `## Requirement:` heading"})
	}
	if !normativeRe.MatchString(logical) {
		missing = append(missing, specElement{"normative-language", "missing normative MUST or SHALL language"})
	}
	if !strings.Contains(logical, "### Scenario:") {
		missing = append(missing, specElement{"scenario-heading", "missing at least one `### Scenario:` section"})
	}
	if !strings.Contains(logical, "**WHEN**") {
		missing = append(missing, specElement{"when-bullet", "missing `**WHEN**` scenario bullet"})
	}
	if !strings.Contains(logical, "**THEN**") {
		missing = append(missing, specElement{"then-bullet", "missing `**THEN**` scenario bullet"})
	}
	return missing
}

// taskCanonicalElements returns the canonical TASK elements missing from an
// already-logical TASK body. TASK discipline requires the `## Task:` heading and
// the `### Execution Planning` PROCESS-planning section so the coordinator can
// decide serial-vs-parallel decomposition from the TASK alone.
func taskCanonicalElements(logical string) []specElement {
	logical = strings.TrimSpace(logical)
	var missing []specElement
	if !strings.Contains(logical, "## Task:") {
		missing = append(missing, specElement{"task-heading", "missing `## Task:` heading"})
	}
	if !strings.Contains(logical, "### Execution Planning") {
		missing = append(missing, specElement{"execution-planning", "missing `### Execution Planning` section (owned areas, coupling class, recommended execution mode)"})
	}
	return missing
}

// processCanonicalElements returns the canonical PROCESS elements missing from an
// already-logical PROCESS body. Every PROCESS node must record its `### Parent
// TASK`; serial-chain `### Handoff` evidence is enforced at verify where the DAG
// is visible.
func processCanonicalElements(logical string) []specElement {
	logical = strings.TrimSpace(logical)
	var missing []specElement
	if !strings.Contains(logical, "## Process:") {
		missing = append(missing, specElement{"process-heading", "missing `## Process:` heading"})
	}
	if !strings.Contains(logical, "### Parent TASK") {
		missing = append(missing, specElement{"parent-task", "missing `### Parent TASK` section"})
	}
	return missing
}

// SpecBodyErrors reports canonical SPEC discipline problems for an already
// logical SPEC body as human-readable messages. It is shared by the durable
// spec renderer so archive and upsert enforce identical rules.
func SpecBodyErrors(logical string) []string {
	elements := specCanonicalElements(logical)
	if len(elements) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(elements))
	for _, e := range elements {
		msgs = append(msgs, e.message)
	}
	return msgs
}

// ValidateCanonicalBody validates a full (possibly wrapped) typed comment body
// for canonical discipline. It extracts the logical body first so raw generated
// bodies and already-wrapped bodies behave consistently. SPEC, TASK, and PROCESS
// have strict blocking rules; REVIEW, VERIFY, and QUESTION return no diagnostics.
func ValidateCanonicalBody(commentType, id, url, body string) []CanonicalDiagnostic {
	return validateCanonicalBody(commentType, id, url, body, "", false)
}

// ValidateCanonicalBodyAtRoot additionally enforces the legacy-existing durable
// target rule when repositoryRoot is non-empty. Missing Durable Intent remains
// mode-dependent proposal policy and is intentionally not a universal canonical
// error.
func ValidateCanonicalBodyAtRoot(commentType, id, url, body, repositoryRoot string) []CanonicalDiagnostic {
	return validateCanonicalBody(commentType, id, url, body, repositoryRoot, true)
}

func validateCanonicalBody(commentType, id, url, body, repositoryRoot string, validateDurable bool) []CanonicalDiagnostic {
	commentType = strings.ToUpper(strings.TrimSpace(commentType))
	logical := LogicalBody(body)
	var elements []specElement
	switch commentType {
	case "SPEC":
		elements = specCanonicalElements(logical)
		if validateDurable {
			if _, found, err := durable.ParseSpecIntent(logical, durable.ValidationOptions{RepositoryRoot: repositoryRoot, SpecID: id}); found && err != nil {
				elements = append(elements, specElement{"durable-intent", err.Error()})
			}
		}
	case "TASK":
		elements = taskCanonicalElements(logical)
	case "PROCESS":
		elements = processCanonicalElements(logical)
	default:
		return nil
	}
	if len(elements) == 0 && commentType != "PROCESS" {
		return nil
	}
	diags := make([]CanonicalDiagnostic, 0, len(elements))
	for _, e := range elements {
		diags = append(diags, CanonicalDiagnostic{
			Severity: "error",
			Type:     commentType,
			ID:       id,
			URL:      url,
			Element:  e.element,
			Message:  e.message,
		})
	}
	if commentType == "PROCESS" {
		for _, diagnostic := range ParseProcessExecutionClass(id, url, body).Diagnostics {
			if diagnostic.Severity == "error" {
				diags = append(diags, diagnostic)
			}
		}
		for _, diagnostic := range ParseProcessWorkspaceManagement(id, url, body).Diagnostics {
			if diagnostic.Severity == "error" {
				diags = append(diags, diagnostic)
			}
		}
	}
	return diags
}

// ParseSpecDurableIntent exposes the strict normalized intent model for status
// and later durable planning without making prose or changed files authoritative.
func ParseSpecDurableIntent(id, body, repositoryRoot string) (durable.Intent, bool, error) {
	return durable.ParseSpecIntent(LogicalBody(body), durable.ValidationOptions{RepositoryRoot: repositoryRoot, SpecID: id})
}

// ValidateArtifact recomputes canonical validity for a parsed typed comment from
// its remote body. Every workflow gate (list/status/verify/archive) uses this so
// a write-time --allow-noncanonical bypass never durably silences diagnostics.
func ValidateArtifact(a Artifact) []CanonicalDiagnostic {
	return ValidateCanonicalBody(a.Comment.Type, a.Comment.ID, a.URL, a.Comment.Body)
}

func ValidateArtifactAtRoot(a Artifact, repositoryRoot string) []CanonicalDiagnostic {
	return ValidateCanonicalBodyAtRoot(a.Comment.Type, a.Comment.ID, a.URL, a.Comment.Body, repositoryRoot)
}

// CanonicalDiagnosticStrings formats diagnostics as actionable one-line strings
// suitable for CLI output and error aggregation.
func CanonicalDiagnosticStrings(diags []CanonicalDiagnostic) []string {
	out := make([]string, 0, len(diags))
	for _, d := range diags {
		id := d.ID
		if id == "" {
			id = d.Type
		}
		if d.URL != "" {
			out = append(out, fmt.Sprintf("%s %s (%s): %s", d.Type, id, d.URL, d.Message))
		} else {
			out = append(out, fmt.Sprintf("%s %s: %s", d.Type, id, d.Message))
		}
	}
	return out
}
