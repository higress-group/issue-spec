package gates

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	// CompactSummarySchemaVersion is intentionally independent of the full
	// Report JSON contract so compact consumers can negotiate additive changes.
	CompactSummarySchemaVersion = 1
	compactAffectedLimit        = 10
	compactArtifactID           = "{artifact_id}"
)

// CompactSummary is a bounded projection of an already evaluated Report. It
// contains no policy and cannot produce a decision different from Report.Ready.
type CompactSummary struct {
	SchemaVersion int                       `json:"schema_version"`
	OK            bool                      `json:"ok"`
	Gate          CompactGate               `json:"gate"`
	Subject       *CompactSubject           `json:"subject,omitempty"`
	Counts        map[string]map[string]int `json:"counts,omitempty"`
	Blockers      []CompactBlockerGroup     `json:"blockers"`
}

type CompactGate struct {
	Target      Target `json:"target"`
	Mode        Mode   `json:"mode"`
	PointInTime bool   `json:"point_in_time"`
}

// CompactSubject identifies the exact provider subject when the command
// collected one. Evidence remains structured rather than a shell/UI string.
type CompactSubject struct {
	Revision string                   `json:"revision,omitempty"`
	Evidence *CompactEvidenceIdentity `json:"evidence,omitempty"`
}

type CompactEvidenceIdentity struct {
	Kind       string `json:"kind"`
	ID         string `json:"id,omitempty"`
	URL        string `json:"url,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Repository string `json:"repository,omitempty"`
}

type CompactAffectedArtifact struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
}

type CompactBlockerGroup struct {
	Code           string                    `json:"code"`
	Count          int                       `json:"count"`
	Affected       []CompactAffectedArtifact `json:"affected"`
	TruncatedCount int                       `json:"truncated_count"`
	Remediation    Remediation               `json:"remediation"`
	Detail         Remediation               `json:"detail"`
}

// ProjectCompactSummary groups only blocking diagnostics from the supplied
// authoritative report. Counts and subject are routing metadata collected by
// the caller; neither can alter OK.
func ProjectCompactSummary(report Report, counts map[string]map[string]int, subject *CompactSubject, detail Remediation) CompactSummary {
	type groupKey struct {
		Code    string
		Family  string
		Variant string
	}
	type accumulator struct {
		count       int
		remediation Remediation
		affected    map[CompactAffectedArtifact]struct{}
	}

	groups := map[groupKey]*accumulator{}
	for _, diagnostic := range report.Diagnostics {
		if !diagnostic.Blocking {
			continue
		}
		remediation := normalizedRemediation(diagnostic)
		variant, _ := json.Marshal(remediation.Arguments)
		// The normalized structured action is complete: artifact type or
		// execution class affects grouping only when represented by a non-ID
		// action argument, in which case it is already part of Variant.
		key := groupKey{Code: diagnostic.Code, Family: remediation.CommandFamily, Variant: string(variant)}
		group := groups[key]
		if group == nil {
			group = &accumulator{remediation: remediation, affected: map[CompactAffectedArtifact]struct{}{}}
			groups[key] = group
		}
		group.count++
		if diagnostic.Artifact.Type != "" || diagnostic.Artifact.ID != "" {
			group.affected[CompactAffectedArtifact{Type: diagnostic.Artifact.Type, ID: diagnostic.Artifact.ID}] = struct{}{}
		}
	}

	keys := make([]groupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Code != keys[j].Code {
			return keys[i].Code < keys[j].Code
		}
		if keys[i].Family != keys[j].Family {
			return keys[i].Family < keys[j].Family
		}
		return keys[i].Variant < keys[j].Variant
	})

	blockers := make([]CompactBlockerGroup, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		affected := make([]CompactAffectedArtifact, 0, len(group.affected))
		for artifact := range group.affected {
			affected = append(affected, artifact)
		}
		sort.Slice(affected, func(i, j int) bool {
			if affected[i].Type != affected[j].Type {
				return affected[i].Type < affected[j].Type
			}
			return affected[i].ID < affected[j].ID
		})
		truncated := 0
		if len(affected) > compactAffectedLimit {
			truncated = len(affected) - compactAffectedLimit
			affected = affected[:compactAffectedLimit]
		}
		blockers = append(blockers, CompactBlockerGroup{
			Code: key.Code, Count: group.count, Affected: affected, TruncatedCount: truncated,
			Remediation: cloneRemediation(group.remediation), Detail: cloneRemediation(detail),
		})
	}

	return CompactSummary{
		SchemaVersion: CompactSummarySchemaVersion,
		OK:            report.Ready,
		Gate:          CompactGate{Target: report.Target, Mode: report.Mode, PointInTime: report.PointInTime},
		Subject:       cloneCompactSubject(subject), Counts: cloneCounts(counts), Blockers: blockers,
	}
}

func normalizedRemediation(diagnostic Diagnostic) Remediation {
	remediation := cloneRemediation(diagnostic.Remediation)
	id := strings.TrimSpace(diagnostic.Artifact.ID)
	if id == "" {
		return remediation
	}
	for index, argument := range remediation.Arguments {
		switch {
		case argument == id:
			remediation.Arguments[index] = compactArtifactID
		case strings.HasSuffix(argument, "="+id):
			remediation.Arguments[index] = strings.TrimSuffix(argument, id) + compactArtifactID
		}
	}
	return remediation
}

func cloneRemediation(value Remediation) Remediation {
	return Remediation{CommandFamily: value.CommandFamily, Arguments: append([]string(nil), value.Arguments...)}
}

func cloneCounts(values map[string]map[string]int) map[string]map[string]int {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]map[string]int, len(values))
	for artifactType, statuses := range values {
		cloned[artifactType] = make(map[string]int, len(statuses))
		for status, count := range statuses {
			cloned[artifactType][status] = count
		}
	}
	return cloned
}

func cloneCompactSubject(subject *CompactSubject) *CompactSubject {
	if subject == nil {
		return nil
	}
	cloned := *subject
	if subject.Evidence != nil {
		evidence := *subject.Evidence
		cloned.Evidence = &evidence
	}
	return &cloned
}
