// Package finalization contains the side-effect-free read model used by final
// lifecycle decisions.
package finalization

import (
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

// SupersessionEdge is one normalized explicit replacement relationship.
type SupersessionEdge struct {
	FromProcessID string `json:"from_process_id"`
	ToProcessID   string `json:"to_process_id"`
	TargetURL     string `json:"target_url"`
}

// HistoricalProcess records the complete explicit chain from a historical
// PROCESS to its unique active sink.
type HistoricalProcess struct {
	ProcessID    string   `json:"process_id"`
	ActiveSinkID string   `json:"active_sink_id"`
	Chain        []string `json:"chain"`
}

// Diagnostic explains why selection retained all PROCESS carriers. Codes and
// identities are stable so callers can present compact or detailed views from
// the same result.
type Diagnostic struct {
	Code            string `json:"code"`
	ProcessID       string `json:"process_id,omitempty"`
	TargetProcessID string `json:"target_process_id,omitempty"`
	URL             string `json:"url,omitempty"`
	Message         string `json:"message"`
}

// Selection is the deterministic active/historical PROCESS read model.
// Invalid is fail-closed: no PROCESS appears in Historical and every unique
// typed PROCESS remains in Active.
type Selection struct {
	Edges                      []SupersessionEdge  `json:"edges"`
	Historical                 []HistoricalProcess `json:"historical"`
	Active                     []model.Artifact    `json:"active"`
	ActiveProcessIDs           []string            `json:"active_process_ids"`
	LegacySupersededProcessIDs []string            `json:"legacy_superseded_process_ids,omitempty"`
	Diagnostics                []Diagnostic        `json:"diagnostics,omitempty"`
}

// Valid reports whether every observed replacement relationship formed an
// explicit same-selection acyclic chain with a unique active sink.
func (s Selection) Valid() bool { return len(s.Diagnostics) == 0 }

// EvaluateSelection computes active and historical PROCESS carriers without
// provider access or writes. The input must be the typed artifacts observed on
// one Implement issue; a marker target outside that set is rejected as a
// cross-change or missing target.
func EvaluateSelection(artifacts []model.Artifact) Selection {
	processes := make([]model.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if strings.EqualFold(strings.TrimSpace(artifact.Comment.Type), "PROCESS") {
			processes = append(processes, artifact)
		}
	}
	sort.SliceStable(processes, func(i, j int) bool {
		if processes[i].Comment.ID != processes[j].Comment.ID {
			return processes[i].Comment.ID < processes[j].Comment.ID
		}
		return processes[i].URL < processes[j].URL
	})

	result := Selection{Edges: []SupersessionEdge{}, Historical: []HistoricalProcess{}, Active: []model.Artifact{}, ActiveProcessIDs: []string{}}
	byID := make(map[string]model.Artifact, len(processes))
	byURL := make(map[string]string, len(processes)*2)
	edges := make(map[string]SupersessionEdge, len(processes))
	unique := make([]model.Artifact, 0, len(processes))

	for _, artifact := range processes {
		id := strings.TrimSpace(artifact.Comment.ID)
		if err := model.ValidateTypedIdentity("PROCESS", id); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "invalid-process-identity", ProcessID: id, URL: artifact.URL, Message: err.Error()})
			continue
		}
		if _, duplicate := byID[id]; duplicate {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "duplicate-process-id", ProcessID: id, URL: artifact.URL, Message: "PROCESS identity is not unique in the selection"})
			continue
		}
		byID[id] = artifact
		unique = append(unique, artifact)
		for _, identity := range []string{artifact.URL, artifact.APIURL} {
			if identity == "" {
				continue
			}
			if previous, exists := byURL[identity]; exists && previous != id {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "duplicate-provider-identity", ProcessID: id, URL: identity, Message: "provider identity is shared by multiple PROCESS carriers"})
				continue
			}
			byURL[identity] = id
		}
	}

	for _, artifact := range unique {
		id := artifact.Comment.ID
		marker, found, err := model.ParseSupersededBy(artifact.Comment.Body, id)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "invalid-superseded-by", ProcessID: id, URL: artifact.URL, Message: err.Error()})
			continue
		}
		if !found {
			if artifact.Comment.Status == "superseded" {
				result.LegacySupersededProcessIDs = append(result.LegacySupersededProcessIDs, id)
			}
			continue
		}
		targetID, exists := byURL[marker.URL]
		if !exists {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "missing-or-cross-change-target", ProcessID: id, TargetProcessID: marker.ProcessID, URL: marker.URL, Message: "superseded-by target is not a PROCESS provider identity in this selection"})
			continue
		}
		if targetID != marker.ProcessID {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "target-identity-mismatch", ProcessID: id, TargetProcessID: marker.ProcessID, URL: marker.URL, Message: fmt.Sprintf("superseded-by URL identifies %s", targetID)})
			continue
		}
		edge := SupersessionEdge{FromProcessID: id, ToProcessID: marker.ProcessID, TargetURL: marker.URL}
		if previous, duplicate := edges[id]; duplicate && previous != edge {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "multiple-successors", ProcessID: id, TargetProcessID: marker.ProcessID, URL: marker.URL, Message: "PROCESS has more than one superseded-by successor"})
			continue
		}
		edges[id] = edge
		result.Edges = append(result.Edges, edge)
	}

	sort.Strings(result.LegacySupersededProcessIDs)
	sort.Slice(result.Edges, func(i, j int) bool {
		if result.Edges[i].FromProcessID != result.Edges[j].FromProcessID {
			return result.Edges[i].FromProcessID < result.Edges[j].FromProcessID
		}
		if result.Edges[i].ToProcessID != result.Edges[j].ToProcessID {
			return result.Edges[i].ToProcessID < result.Edges[j].ToProcessID
		}
		return result.Edges[i].TargetURL < result.Edges[j].TargetURL
	})

	if len(result.Diagnostics) == 0 {
		for source := range edges {
			chain, cycle := resolveChain(source, edges)
			if cycle {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "supersession-cycle", ProcessID: source, Message: "superseded-by chain is cyclic"})
				continue
			}
			result.Historical = append(result.Historical, HistoricalProcess{ProcessID: source, ActiveSinkID: chain[len(chain)-1], Chain: chain})
		}
	}

	sortDiagnostics(result.Diagnostics)
	if len(result.Diagnostics) != 0 {
		result.Historical = []HistoricalProcess{}
		result.Active = append(result.Active, unique...)
	} else {
		sort.Slice(result.Historical, func(i, j int) bool { return result.Historical[i].ProcessID < result.Historical[j].ProcessID })
		for _, artifact := range unique {
			if _, historical := edges[artifact.Comment.ID]; !historical {
				result.Active = append(result.Active, artifact)
			}
		}
	}
	for _, artifact := range result.Active {
		result.ActiveProcessIDs = append(result.ActiveProcessIDs, artifact.Comment.ID)
	}
	return result
}

// Select is a concise alias for EvaluateSelection for consumers that already
// operate in the finalization package vocabulary.
func Select(artifacts []model.Artifact) Selection { return EvaluateSelection(artifacts) }

func resolveChain(source string, edges map[string]SupersessionEdge) ([]string, bool) {
	chain := []string{source}
	seen := map[string]bool{source: true}
	current := source
	for {
		edge, exists := edges[current]
		if !exists {
			return chain, false
		}
		if seen[edge.ToProcessID] {
			return chain, true
		}
		current = edge.ToProcessID
		seen[current] = true
		chain = append(chain, current)
	}
}

func sortDiagnostics(values []Diagnostic) {
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i], values[j]
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.ProcessID != right.ProcessID {
			return left.ProcessID < right.ProcessID
		}
		if left.TargetProcessID != right.TargetProcessID {
			return left.TargetProcessID < right.TargetProcessID
		}
		return left.URL < right.URL
	})
}
