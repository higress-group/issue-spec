package model

import (
	"fmt"
	"sort"
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
	byURL := map[string]Artifact{}
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
		if artifact.URL != "" {
			byURL[NormalizeURL(artifact.URL)] = artifact
		}
		if artifact.APIURL != "" {
			byURL[NormalizeURL(artifact.APIURL)] = artifact
		}
	}

	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.ID == "" {
			continue
		}
		for _, link := range RelatedCommentURLs(tc) {
			if _, ok := byURL[NormalizeURL(link)]; !ok {
				report.Errors = append(report.Errors, fmt.Sprintf("%s links unknown related comment %s", displayID(tc), link))
			}
		}
		switch tc.Type {
		case "TASK":
			specs := linkedArtifactsOfType(tc, byURL, "SPEC")
			if len(specs) == 0 {
				report.Errors = append(report.Errors, fmt.Sprintf("%s must link at least one SPEC comment", displayID(tc)))
			}
			for _, spec := range specs {
				if !hasRelatedBacklink(spec.Comment, artifact.URL, artifact.APIURL) {
					report.Errors = append(report.Errors, fmt.Sprintf("%s must backlink %s", displayID(spec.Comment), displayID(tc)))
				}
			}
		case "PROCESS":
			tasks := linkedArtifactsOfType(tc, byURL, "TASK")
			if len(tasks) == 0 {
				report.Errors = append(report.Errors, fmt.Sprintf("%s must link at least one TASK comment", displayID(tc)))
			}
			for _, task := range tasks {
				if !hasRelatedBacklink(task.Comment, artifact.URL, artifact.APIURL) {
					report.Errors = append(report.Errors, fmt.Sprintf("%s must backlink %s", displayID(task.Comment), displayID(tc)))
				}
			}
		}
	}

	sort.Strings(report.Errors)
	sort.Strings(report.Warnings)
	report.OK = len(report.Errors) == 0
	return report
}

func linkedArtifactsOfType(tc TypedComment, byURL map[string]Artifact, want string) []Artifact {
	var out []Artifact
	seen := map[string]bool{}
	for _, link := range RelatedCommentURLs(tc) {
		artifact, ok := byURL[NormalizeURL(link)]
		if !ok || artifact.Comment.Type != want {
			continue
		}
		key := artifact.Comment.ID + artifact.URL
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, artifact)
	}
	return out
}

func hasRelatedBacklink(tc TypedComment, urls ...string) bool {
	want := map[string]bool{}
	for _, url := range urls {
		if url != "" {
			want[NormalizeURL(url)] = true
		}
	}
	for _, link := range RelatedCommentURLs(tc) {
		if want[NormalizeURL(link)] {
			return true
		}
	}
	return false
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
