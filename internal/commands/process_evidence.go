package commands

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

var processTestEvidencePattern = regexp.MustCompile(`(?i)\btest(s|ing|ed)?\b`)

func buildProcessEvidenceInputs(artifacts []model.Artifact, prURL string, reviewComments []github.PullRequestReviewComment,
	review reviewSyncReport, external *externalEvidenceConsumption) []gates.ProcessEvidenceInput {
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
	externalValid := external != nil && validateExternalEvidenceConsumption(*external, processes, activeSpecs) == nil
	processByID := make(map[string]model.Artifact, len(processes))
	for _, process := range processes {
		processByID[process.Comment.ID] = process
	}
	authorAgentsBySpec := map[string]map[string]bool{}
	for _, comment := range reviewComments {
		marker, ok, err := model.FindRationaleMarker(comment.Body)
		if err != nil || !ok {
			continue
		}
		agent := strings.ToLower(strings.TrimSpace(marker.Agent))
		spec := strings.TrimSpace(marker.Spec)
		if agent == "" || spec == "" {
			continue
		}
		// Credit an author identity only from a genuine change-bearing carrier,
		// mirroring the gate's change-bearing validation: real path/line, active
		// SPEC, matching SPEC URL, and an active (non-superseded) change-bearing
		// PROCESS that actually covers the SPEC. Otherwise a stale, forged, or
		// wrong-class marker could pollute the author set and falsely block an
		// independent reviewer of that SPEC.
		if marker.Path == "" || marker.Line <= 0 || marker.Path != comment.Path || marker.Line != comment.Line {
			continue
		}
		want, active := activeSpecs[spec]
		if !active {
			continue
		}
		if specURL := rationaleSpecURL(comment.Body); specURL == "" || model.NormalizeURL(specURL) != model.NormalizeURL(want) {
			continue
		}
		process, resolved := processByID[strings.TrimSpace(marker.Process)]
		if !resolved {
			continue
		}
		if model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body).Class != model.ProcessExecutionChangeBearing {
			continue
		}
		if !artifactReferencesSpec(process, spec, want) {
			continue
		}
		if authorAgentsBySpec[spec] == nil {
			authorAgentsBySpec[spec] = map[string]bool{}
		}
		authorAgentsBySpec[spec][agent] = true
	}
	inputs := make([]gates.ProcessEvidenceInput, 0, len(processes))
	for _, process := range processes {
		input := gates.ProcessEvidenceInput{Process: process, RequiredPRURL: prURL, ActiveSpecs: activeSpecs, TaskURLs: taskURLs,
			AuthorAgentsBySpec: authorAgentsBySpec}
		for _, comment := range reviewComments {
			marker, ok, err := model.FindRationaleMarker(comment.Body)
			if err != nil || !ok || marker.Process != process.Comment.ID {
				continue
			}
			input.Rationales = append(input.Rationales, gates.RationaleEvidence{ProcessID: marker.Process, SpecID: marker.Spec,
				SpecURL: rationaleSpecURL(comment.Body), MarkerPath: marker.Path, MarkerLine: marker.Line,
				CommentPath: comment.Path, CommentLine: comment.Line, AuthorAgent: marker.Agent})
		}
		for _, artifact := range reviews {
			if !artifactReferencesProcess(artifact, process) {
				continue
			}
			for specID := range activeSpecs {
				if artifactReferencesSpec(artifact, specID, activeSpecs[specID]) {
					input.Reviews = append(input.Reviews, gates.ReviewEvidence{ProcessID: process.Comment.ID, SpecID: specID, URL: artifact.URL,
						Done: true, ReviewerAgent: artifact.Comment.Agent, Source: "typed-review"})
				}
			}
		}
		for _, finding := range review.ResolvedFindings {
			if finding.Process == process.Comment.ID {
				input.Reviews = append(input.Reviews, gates.ReviewEvidence{ProcessID: finding.Process, SpecID: finding.Spec, URL: finding.URL,
					FindingResolved: true, ReviewerAgent: finding.Agent, SubjectRevision: finding.SubjectRevision, Trusted: finding.SubjectRevision != "", Source: finding.RevisionSource})
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
		if externalValid {
			input.External = externalProcessEvidenceFor(process.Comment.ID, activeSpecs, *external)
		}
		inputs = append(inputs, input)
	}
	return inputs
}

func validateExternalEvidenceConsumption(consumption externalEvidenceConsumption, processes []model.Artifact, activeSpecs map[string]string) error {
	revision := strings.TrimSpace(consumption.SubjectRevision)
	if revision == "" || consumption.SubjectRevision != revision || len(consumption.Bindings) == 0 {
		return fmt.Errorf("external evidence consumption has no revision-bound bindings")
	}
	selected := make(map[string]bool, len(consumption.EvidenceIDs))
	for _, raw := range consumption.EvidenceIDs {
		id := strings.TrimSpace(raw)
		if id == "" || id != raw || selected[id] {
			return fmt.Errorf("external evidence selection contains an invalid or duplicate id %q", raw)
		}
		selected[id] = true
	}
	processByID := make(map[string]model.Artifact, len(processes))
	for _, process := range processes {
		id := process.Comment.ID
		if !externalProcessIDPattern.MatchString(id) || processByID[id].Comment.ID != "" {
			return fmt.Errorf("active PROCESS identity %q is invalid or ambiguous", id)
		}
		processByID[id] = process
	}
	bound := make(map[string]bool, len(consumption.Bindings))
	for _, binding := range consumption.Bindings {
		if !externalProcessIDPattern.MatchString(binding.ProcessID) || !externalSpecIDPattern.MatchString(binding.SpecID) ||
			strings.TrimSpace(binding.EvidenceID) == "" || binding.EvidenceID != strings.TrimSpace(binding.EvidenceID) ||
			!selected[binding.EvidenceID] || bound[binding.EvidenceID] || !binding.Trusted ||
			binding.Source != "native-authoritative-ledger" || binding.SubjectRevision != revision ||
			(binding.Kind != codereview.EvidenceReview && binding.Kind != codereview.EvidenceCheck) {
			return fmt.Errorf("external evidence binding %q is invalid, conflicting, or stale", binding.EvidenceID)
		}
		process, processOK := processByID[binding.ProcessID]
		specURL, specOK := activeSpecs[binding.SpecID]
		if !processOK || !specOK || !artifactReferencesSpec(process, binding.SpecID, specURL) {
			return fmt.Errorf("external evidence binding %q does not map to an active PROCESS/SPEC edge", binding.EvidenceID)
		}
		bound[binding.EvidenceID] = true
	}
	return nil
}

func externalProcessEvidenceFor(processID string, activeSpecs map[string]string, consumption externalEvidenceConsumption) []gates.ExternalProcessEvidence {
	selected := make(map[string]bool, len(consumption.EvidenceIDs))
	for _, id := range consumption.EvidenceIDs {
		selected[strings.TrimSpace(id)] = true
	}
	var result []gates.ExternalProcessEvidence
	invalid := false
	seen := map[string]bool{}
	for _, binding := range consumption.Bindings {
		if strings.TrimSpace(binding.ProcessID) != processID {
			continue
		}
		_, activeSpec := activeSpecs[strings.TrimSpace(binding.SpecID)]
		validKind := binding.Kind == codereview.EvidenceReview || binding.Kind == codereview.EvidenceCheck
		valid := externalProcessIDPattern.MatchString(binding.ProcessID) && externalSpecIDPattern.MatchString(binding.SpecID) &&
			activeSpec && strings.TrimSpace(binding.EvidenceID) != "" && selected[strings.TrimSpace(binding.EvidenceID)] &&
			binding.Trusted && strings.TrimSpace(binding.SubjectRevision) != "" &&
			strings.TrimSpace(binding.SubjectRevision) == strings.TrimSpace(consumption.SubjectRevision) && validKind &&
			binding.Source == "native-authoritative-ledger"
		if !valid {
			invalid = true
			continue
		}
		key := binding.SpecID + "\x00" + binding.EvidenceID + "\x00" + string(binding.Kind)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, gates.ExternalProcessEvidence{ProcessID: processID, SpecID: binding.SpecID,
			SubjectRevision: consumption.SubjectRevision, EvidenceRevision: binding.SubjectRevision, Consumed: true,
			EvidenceIDs: []string{binding.EvidenceID}, Trusted: true, Source: binding.Source + ":" + binding.EvidenceID})
	}
	if invalid {
		return nil
	}
	return result
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
