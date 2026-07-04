package model

import (
	"fmt"
	"regexp"
	"strings"
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
// bodies and already-wrapped bodies behave consistently. Only SPEC has strict
// blocking rules today; other types return no diagnostics.
func ValidateCanonicalBody(commentType, id, url, body string) []CanonicalDiagnostic {
	commentType = strings.ToUpper(strings.TrimSpace(commentType))
	if commentType != "SPEC" {
		return nil
	}
	logical := LogicalBody(body)
	elements := specCanonicalElements(logical)
	if len(elements) == 0 {
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
	return diags
}

// ValidateArtifact recomputes canonical validity for a parsed typed comment from
// its remote body. Every workflow gate (list/status/verify/archive) uses this so
// a write-time --allow-noncanonical bypass never durably silences diagnostics.
func ValidateArtifact(a Artifact) []CanonicalDiagnostic {
	return ValidateCanonicalBody(a.Comment.Type, a.Comment.ID, a.URL, a.Comment.Body)
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
