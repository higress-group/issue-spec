package model

import (
	"fmt"
	"sort"
	"strings"
)

type Artifact struct {
	Issue     int                   `json:"issue"`
	CommentID int64                 `json:"comment_id"`
	URL       string                `json:"url"`
	APIURL    string                `json:"api_url,omitempty"`
	Comment   TypedComment          `json:"comment"`
	Canonical []CanonicalDiagnostic `json:"canonical,omitempty"`
}

type VerifyReport struct {
	OK       bool     `json:"ok"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// TraceabilityEdge is a model-neutral projection of one canonical relationship
// edge.  The relationship registry lives above model, so callers inject the
// already-built index without creating an import cycle or reinterpreting raw
// Related Comments links here.
type TraceabilityEdge struct {
	Kind     string `json:"kind"`
	OwnerID  string `json:"owner_id"`
	TargetID string `json:"target_id"`
}

func VerifyTraceability(artifacts []Artifact) VerifyReport {
	// Preserve the public model-only helper for compatibility. Command and gate
	// readers use VerifyTraceabilityWithRelationships with the shared index.
	return VerifyTraceabilityWithRelationships(artifacts, legacyTraceabilityEdges(artifacts), nil)
}

// VerifyTraceabilityWithRelationships validates semantic planning against the
// supplied canonical owner edges. Missing, stale, or extra reverse links never
// enter this view and therefore cannot affect readiness.
func VerifyTraceabilityWithRelationships(artifacts []Artifact, edges []TraceabilityEdge, relationshipErr error) VerifyReport {
	report := VerifyReport{OK: true}
	if relationshipErr != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("canonical relationship index: %v", relationshipErr))
	}
	byID := map[string]Artifact{}
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.ID == "" {
			continue
		}
		for _, parseErr := range tc.Errors {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %s", displayID(tc), parseErr))
		}
		if previous, exists := byID[tc.ID]; exists {
			report.Errors = append(report.Errors, fmt.Sprintf("duplicate logical id %s on %s and %s", tc.ID, previous.URL, artifact.URL))
		}
		byID[tc.ID] = artifact
	}

	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.ID == "" {
			continue
		}
		switch tc.Type {
		case "TASK":
			verifyTaskCoverage(&report, tc, byID, edges)
		case "PROCESS":
			verifyProcessPlanning(&report, tc, byID, edges)
		}
	}

	sort.Strings(report.Errors)
	sort.Strings(report.Warnings)
	report.OK = len(report.Errors) == 0
	return report
}

func verifyTaskCoverage(report *VerifyReport, task TypedComment, byID map[string]Artifact, edges []TraceabilityEdge) {
	covers := TypedSectionList(task.Body, "### Covers")
	if len(covers) == 0 {
		report.Errors = append(report.Errors, fmt.Sprintf("%s must cover at least one SPEC", displayID(task)))
		return
	}
	seen := map[string]bool{}
	for _, specID := range covers {
		if seen[specID] {
			report.Errors = append(report.Errors, fmt.Sprintf("%s covers duplicate SPEC %s", displayID(task), specID))
			continue
		}
		seen[specID] = true
		spec, ok := byID[specID]
		if !ok || spec.Comment.Type != "SPEC" {
			report.Errors = append(report.Errors, fmt.Sprintf("%s covers unknown SPEC %s", displayID(task), specID))
			continue
		}
		if !hasTraceabilityEdge(edges, "task-covers-spec", task.ID, specID) {
			report.Errors = append(report.Errors, fmt.Sprintf("%s coverage for %s is missing its canonical SPEC URL", displayID(task), specID))
		}
	}
}

func hasArtifactURL(values []string, artifact Artifact) bool {
	want := map[string]bool{}
	for _, value := range []string{artifact.URL, artifact.APIURL} {
		if normalized := NormalizeURL(value); normalized != "" {
			want[normalized] = true
		}
	}
	for _, value := range values {
		if want[NormalizeURL(value)] {
			return true
		}
	}
	return false
}

func verifyProcessPlanning(report *VerifyReport, process TypedComment, byID map[string]Artifact, edges []TraceabilityEdge) {
	parents := TypedSectionList(process.Body, "### Parent TASK")
	if len(parents) != 1 {
		report.Errors = append(report.Errors, fmt.Sprintf("%s must name exactly one Parent TASK", displayID(process)))
		return
	}
	parent, ok := byID[parents[0]]
	if !ok || parent.Comment.Type != "TASK" {
		report.Errors = append(report.Errors, fmt.Sprintf("%s names unknown Parent TASK %s", displayID(process), parents[0]))
		return
	}
	if !hasTraceabilityEdge(edges, "process-parent-task", process.ID, parent.Comment.ID) {
		report.Errors = append(report.Errors, fmt.Sprintf("%s Parent TASK %s is missing its canonical TASK URL", displayID(process), parent.Comment.ID))
	}
	for _, dependencyID := range TypedSectionList(process.Body, "### Dependencies") {
		if dependencyID == "" || strings.EqualFold(dependencyID, "N/A") {
			continue
		}
		dependency, exists := byID[dependencyID]
		if !exists || dependency.Comment.Type != "PROCESS" {
			report.Errors = append(report.Errors, fmt.Sprintf("%s depends on unknown PROCESS %s", displayID(process), dependencyID))
			continue
		}
		if !hasTraceabilityEdge(edges, "process-depends-on-process", process.ID, dependencyID) {
			report.Errors = append(report.Errors, fmt.Sprintf("%s dependency %s is missing its canonical PROCESS URL", displayID(process), dependencyID))
		}
	}
	taskSpecs := map[string]bool{}
	for _, specID := range TypedSectionList(parent.Comment.Body, "### Covers") {
		taskSpecs[specID] = true
	}
	selectors := map[string]bool{}
	for _, coveredID := range TypedSectionList(process.Body, "### Covers") {
		// Legacy PROCESS bodies used the parent TASK ID as their Covers value.
		// It remains readable, but only SPEC IDs are selector authority.
		if strings.HasPrefix(coveredID, "SPEC-") {
			selectors[coveredID] = true
		}
	}
	if process.Assignment != nil {
		for _, selector := range process.Assignment.ScenarioSelectors {
			selectors[strings.TrimSpace(selector.SpecID)] = true
		}
	}
	for specID := range selectors {
		if specID == "" || !taskSpecs[specID] {
			report.Errors = append(report.Errors, fmt.Sprintf("%s selector %s is outside Parent TASK %s coverage", displayID(process), specID, parent.Comment.ID))
		}
	}
}

func hasTraceabilityEdge(edges []TraceabilityEdge, kind, ownerID, targetID string) bool {
	for _, edge := range edges {
		if edge.Kind == kind && edge.OwnerID == ownerID && edge.TargetID == targetID {
			return true
		}
	}
	return false
}

func legacyTraceabilityEdges(artifacts []Artifact) []TraceabilityEdge {
	byID := map[string]Artifact{}
	for _, artifact := range artifacts {
		if artifact.Comment.ID != "" {
			byID[artifact.Comment.ID] = artifact
		}
	}
	var result []TraceabilityEdge
	for _, artifact := range artifacts {
		tc := artifact.Comment
		appendLinked := func(kind, targetID string) {
			target, ok := byID[targetID]
			if ok && hasArtifactURL(tc.Links["Related Comments"], target) {
				result = append(result, TraceabilityEdge{Kind: kind, OwnerID: tc.ID, TargetID: targetID})
			}
		}
		switch tc.Type {
		case "TASK":
			for _, targetID := range TypedSectionList(tc.Body, "### Covers") {
				appendLinked("task-covers-spec", targetID)
			}
		case "PROCESS":
			for _, targetID := range TypedSectionList(tc.Body, "### Parent TASK") {
				// The model-only compatibility reader historically did not
				// validate PROCESS owner URLs. Indexed readers do.
				result = append(result, TraceabilityEdge{Kind: "process-parent-task", OwnerID: tc.ID, TargetID: targetID})
			}
			for _, targetID := range TypedSectionList(tc.Body, "### Dependencies") {
				result = append(result, TraceabilityEdge{Kind: "process-depends-on-process", OwnerID: tc.ID, TargetID: targetID})
			}
		}
	}
	return result
}

func displayID(tc TypedComment) string {
	if tc.ID != "" {
		return tc.ID
	}
	if tc.Type != "" {
		return tc.Type
	}
	return "typed comment"
}
