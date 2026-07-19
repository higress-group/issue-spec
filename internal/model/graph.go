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

func VerifyTraceability(artifacts []Artifact) VerifyReport {
	report := VerifyReport{OK: true}
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
			verifyTaskCoverage(&report, tc, byID)
		case "PROCESS":
			verifyProcessPlanning(&report, tc, byID)
		}
	}

	sort.Strings(report.Errors)
	sort.Strings(report.Warnings)
	report.OK = len(report.Errors) == 0
	return report
}

func verifyTaskCoverage(report *VerifyReport, task TypedComment, byID map[string]Artifact) {
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
		if !hasArtifactURL(task.Links["Related Comments"], spec) {
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

func verifyProcessPlanning(report *VerifyReport, process TypedComment, byID map[string]Artifact) {
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

func displayID(tc TypedComment) string {
	if tc.ID != "" {
		return tc.ID
	}
	if tc.Type != "" {
		return tc.Type
	}
	return "typed comment"
}
