package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	phaseOrderConflictDiagnostic          = "phase_order_conflict"
	openDecisionCarrierConflictDiagnostic = "open_decision_carrier_conflict"
)

var (
	questionAfterSPECPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?s)\bQUESTION(?:\s+typed)?\s+comments?\b.{0,120}?\b[Aa]fter\b.{0,80}?\bSPEC(?:\s+typed)?\s+comments?\b`),
		regexp.MustCompile(`(?s)\bSPEC\b.{0,120}?\b[Bb]efore\b.{0,80}?\bQUESTION\b`),
		regexp.MustCompile(`(?s)\bSPEC(?:\s+typed)?\s+comments?\b.{0,180}?\b[Ff]irst\b.{0,180}?\b[Tt]hen\b.{0,100}?\bQUESTION(?:\s+typed)?\s+comments?\b`),
		regexp.MustCompile(`\bSPEC\b\s*(?:->|→)\s*\bQUESTION\b`),
	}
	openDecisionPattern  = regexp.MustCompile(`(?i)\b(?:open|unresolved|remaining)\s+(?:questions?|decisions?|choices?|plans?)\b`)
	bodyCarrierPattern   = regexp.MustCompile(`(?i)\b(?:add|capture|carry|contain|cover|document|include|list|put|record|write)\w*\b`)
	typedQuestionPattern = regexp.MustCompile(`(?i)\b(?:blocking\s+typed\s+QUESTION|typed\s+QUESTION(?:\s+comments?)?|QUESTION\s+typed\s+comments?)\b`)
	issueBodyPattern     = regexp.MustCompile(`(?i)\b(?:(?:proposal|design|implement|issue)[ -]?body|(?:proposal|design|implement)\s+issue)\b`)
)

type protocolTextSource struct {
	locator   string
	path      string
	artifact  string
	text      string
	issueBody bool
}

func workflowProtocolDiagnostics(config Config, artifacts []Artifact, configPath, schemaPath string) []Diagnostic {
	sources := make([]protocolTextSource, 0)
	appendProtocolTextSources(&sources, "context", map[string]any(config.Context), configPath, "", false)
	appendProtocolTextSources(&sources, "rules", config.Rules, configPath, "", false)
	for _, artifact := range artifacts {
		instructions := strings.TrimSpace(artifact.Instructions)
		if instructions == "" {
			continue
		}
		sources = append(sources, protocolTextSource{
			locator:   "artifacts." + artifact.ID + ".instructions",
			path:      schemaPath,
			artifact:  artifact.ID,
			text:      instructions,
			issueBody: issueBodyArtifactType(artifact.Type),
		})
	}

	diagnostics := make([]Diagnostic, 0)
	for _, source := range sources {
		if reversesQuestionSPECOrder(source.text) {
			diagnostics = append(diagnostics, protocolDiagnostic(source, phaseOrderConflictDiagnostic,
				"places QUESTION authoring after SPEC authoring; the built-in phase order is authoritative and project guidance may not reorder or omit enabled phase steps"))
		}
		if carriesOpenDecisionInIssueBody(source) {
			diagnostics = append(diagnostics, protocolDiagnostic(source, openDecisionCarrierConflictDiagnostic,
				"assigns an unresolved decision to issue-body prose; every genuine unresolved decision must use a blocking typed QUESTION carrier"))
		}
	}
	return diagnostics
}

func appendProtocolTextSources(out *[]protocolTextSource, locator string, value any, path, artifact string, issueBody bool) {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			*out = append(*out, protocolTextSource{locator: locator, path: path, artifact: artifact, text: text, issueBody: issueBody})
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := key
			if locator != "" {
				child = locator + "." + key
			}
			appendProtocolTextSources(out, child, typed[key], path, artifact, issueBody || issueBodyRuleLocator(child))
		}
	case []any:
		for index, item := range typed {
			appendProtocolTextSources(out, locator+"["+strconv.Itoa(index)+"]", item, path, artifact, issueBody)
		}
	case []string:
		for index, item := range typed {
			appendProtocolTextSources(out, locator+"["+strconv.Itoa(index)+"]", item, path, artifact, issueBody)
		}
	}
}

func reversesQuestionSPECOrder(text string) bool {
	for _, pattern := range questionAfterSPECPatterns {
		for _, match := range pattern.FindAllStringIndex(text, -1) {
			if !protocolMatchIsNegated(text, match[0]) {
				return true
			}
		}
	}
	return false
}

func carriesOpenDecisionInIssueBody(source protocolTextSource) bool {
	if !source.issueBody && !issueBodyPattern.MatchString(source.text) {
		return false
	}
	for _, match := range openDecisionPattern.FindAllStringIndex(source.text, -1) {
		windowStart, windowEnd := protocolClauseWindow(source.text, match[0], match[1])
		window := source.text[windowStart:windowEnd]
		if !bodyCarrierPattern.MatchString(window) || typedQuestionPattern.MatchString(window) {
			continue
		}
		if !protocolMatchIsNegated(source.text, bodyCarrierPattern.FindStringIndex(window)[0]+windowStart) {
			return true
		}
	}
	return false
}

func protocolDiagnostic(source protocolTextSource, code, conflict string) Diagnostic {
	return Diagnostic{
		Severity: "warning",
		Code:     code,
		Message:  fmt.Sprintf("project workflow text %s %s", source.locator, conflict),
		Path:     source.path,
		Artifact: source.artifact,
		Source:   source.locator,
	}
}

func protocolMatchIsNegated(text string, start int) bool {
	clauseStart := start
	for clauseStart > 0 {
		if strings.ContainsRune(".;:\n", rune(text[clauseStart-1])) {
			break
		}
		clauseStart--
	}
	prefix := strings.ToLower(text[clauseStart:start])
	return strings.Contains(prefix, " not ") || strings.HasPrefix(strings.TrimSpace(prefix), "not ") ||
		strings.Contains(prefix, "never ") || strings.Contains(prefix, "avoid ")
}

func protocolClauseWindow(text string, start, end int) (int, int) {
	for start > 0 && end-start < 240 {
		if strings.ContainsRune(".;\n", rune(text[start-1])) {
			break
		}
		start--
	}
	for end < len(text) && end-start < 240 {
		if strings.ContainsRune(".;\n", rune(text[end])) {
			break
		}
		end++
	}
	return start, end
}

func issueBodyArtifactType(artifactType string) bool {
	switch strings.ToLower(strings.TrimSpace(artifactType)) {
	case "proposal", "design", "implement":
		return true
	default:
		return false
	}
}

func issueBodyRuleLocator(locator string) bool {
	lower := strings.ToLower(locator)
	for _, segment := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == '.' || r == '[' || r == ']'
	}) {
		switch segment {
		case "proposal", "design", "implement":
			return true
		}
	}
	return false
}
