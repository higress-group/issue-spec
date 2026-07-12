package commands

import (
	"regexp"
	"strings"

	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

var processTestEvidencePattern = regexp.MustCompile(`(?i)\btest(s|ing|ed)?\b`)

func buildProcessEvidenceInputs(artifacts []model.Artifact, prURL string, reviewComments []github.PullRequestReviewComment,
	review reviewSyncReport, _ *externalEvidenceConsumption) []gates.ProcessEvidenceInput {
	activeSpecs := map[string]string{}
	taskURLs := map[string]bool{}
	var processes, reviews, verifications []model.Artifact
	for _, artifact := range artifacts {
		if artifact.Comment.Status == "superseded" {
			continue
		}
		switch artifact.Comment.Type {
		case "SPEC":
			activeSpecs[artifact.Comment.ID] = artifact.URL
		case "TASK":
			taskURLs[model.NormalizeURL(artifact.URL)] = true
		case "PROCESS":
			processes = append(processes, artifact)
		case "REVIEW":
			if artifact.Comment.Status == "done" {
				reviews = append(reviews, artifact)
			}
		case "VERIFY":
			if artifact.Comment.Status == "done" {
				verifications = append(verifications, artifact)
			}
		}
	}
	inputs := make([]gates.ProcessEvidenceInput, 0, len(processes))
	for _, process := range processes {
		input := gates.ProcessEvidenceInput{Process: process, RequiredPRURL: prURL, ActiveSpecs: activeSpecs, TaskURLs: taskURLs}
		for _, comment := range reviewComments {
			marker, ok, err := model.FindRationaleMarker(comment.Body)
			if err != nil || !ok || marker.Process != process.Comment.ID {
				continue
			}
			input.Rationales = append(input.Rationales, gates.RationaleEvidence{ProcessID: marker.Process, SpecID: marker.Spec,
				SpecURL: rationaleSpecURL(comment.Body), MarkerPath: marker.Path, MarkerLine: marker.Line,
				CommentPath: comment.Path, CommentLine: comment.Line})
		}
		for _, artifact := range reviews {
			if !artifactReferencesProcess(artifact, process) {
				continue
			}
			for specID := range activeSpecs {
				if artifactReferencesSpec(artifact, specID, activeSpecs[specID]) {
					input.Reviews = append(input.Reviews, gates.ReviewEvidence{ProcessID: process.Comment.ID, SpecID: specID, URL: artifact.URL,
						Done: true, Source: "typed-review"})
				}
			}
		}
		for _, finding := range review.ResolvedFindings {
			if finding.Process == process.Comment.ID {
				input.Reviews = append(input.Reviews, gates.ReviewEvidence{ProcessID: finding.Process, SpecID: finding.Spec, URL: finding.URL,
					FindingResolved: true, SubjectRevision: finding.SubjectRevision, Trusted: finding.SubjectRevision != "", Source: finding.RevisionSource})
			}
		}
		for _, artifact := range verifications {
			if !artifactReferencesProcess(artifact, process) {
				continue
			}
			for specID := range activeSpecs {
				if artifactReferencesSpec(artifact, specID, activeSpecs[specID]) {
					input.Verifications = append(input.Verifications, gates.VerificationEvidence{ProcessID: process.Comment.ID,
						SpecID: specID, URL: artifact.URL, Done: true, TestEvidence: processTestEvidencePattern.MatchString(artifact.Comment.Body), Source: "typed-verify"})
				}
			}
		}
		for _, check := range review.PassedChecks {
			if !strings.Contains(process.Comment.Body, check.Name) {
				continue
			}
			for specID := range activeSpecs {
				testEvidence := processTestEvidencePattern.MatchString(check.Name)
				for _, verify := range input.Verifications {
					if verify.SpecID == specID && verify.TestEvidence {
						testEvidence = true
					}
				}
				if gates.ReferencesArtifactID(process.Comment.Body, specID) {
					input.Checks = append(input.Checks, gates.CheckEvidence{ProcessID: process.Comment.ID, SpecID: specID, Name: check.Name,
						Required: true, Passed: true, TestEvidence: testEvidence, SubjectRevision: check.SubjectRevision, Trusted: check.Trusted, Source: check.Source})
				}
			}
		}
		// External consumption has no structured PROCESS/SPEC binding yet. Never
		// infer one from PROCESS body substrings; fail closed until the adapter
		// ledger exposes an explicit mapping for each consumed evidence ID.
		inputs = append(inputs, input)
	}
	return inputs
}

func rationaleSpecURL(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Spec Comment:"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func artifactReferencesProcess(artifact, process model.Artifact) bool {
	return gates.ReferencesArtifactID(artifact.Comment.Body, process.Comment.ID) || linksContainURL(artifact.Comment.Links["Related Comments"], process.URL)
}

func artifactReferencesSpec(artifact model.Artifact, specID, specURL string) bool {
	return gates.ReferencesArtifactID(artifact.Comment.Body, specID) || linksContainURL(artifact.Comment.Links["Related Comments"], specURL)
}

func linksContainURL(values []string, want string) bool {
	want = model.NormalizeURL(want)
	for _, value := range values {
		if model.NormalizeURL(value) == want {
			return true
		}
	}
	return false
}
