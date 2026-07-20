package durable

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/reconcile/filecas"
)

type AuthorityObservation struct {
	BaselineRevision string            `json:"baseline_revision"`
	Workflow         WorkflowAuthority `json:"workflow"`
	Sources          []SourceAuthority `json:"sources"`
}

type ApplyResult struct {
	OK         bool                    `json:"ok"`
	PlanDigest string                  `json:"plan_digest"`
	Files      filecas.FileApplyResult `json:"files"`
}

// ApplyPlan validates every frozen non-file authority before invoking the
// whole-file CAS engine. No filesystem write occurs on expected-digest,
// workflow, baseline, source, blocker, or target preflight failures.
func ApplyPlan(root string, plan Plan, expectedDigest string, observed AuthorityObservation) (ApplyResult, error) {
	if err := ValidatePlan(plan); err != nil {
		return ApplyResult{}, fmt.Errorf("validate frozen durable plan: %w", err)
	}
	expectedDigest = strings.TrimSpace(expectedDigest)
	if !isPlanDigest(expectedDigest) || expectedDigest != plan.PlanDigest {
		return ApplyResult{}, fmt.Errorf("expected plan digest does not match frozen plan %s", plan.PlanDigest)
	}
	if len(plan.Blockers) != 0 {
		return ApplyResult{PlanDigest: plan.PlanDigest}, errors.New("frozen durable plan has blockers; apply refused")
	}
	if observed.BaselineRevision != plan.BaselineRevision {
		return ApplyResult{PlanDigest: plan.PlanDigest}, fmt.Errorf("baseline drift: planned=%s observed=%s", plan.BaselineRevision, observed.BaselineRevision)
	}
	if observed.Workflow != plan.Workflow {
		return ApplyResult{PlanDigest: plan.PlanDigest}, fmt.Errorf("workflow config drift: planned=%s observed=%s",
			plan.Workflow.ConfigDigest, observed.Workflow.ConfigDigest)
	}
	plannedSources := append([]SourceAuthority(nil), plan.Sources...)
	observedSources := append([]SourceAuthority(nil), observed.Sources...)
	sort.Slice(plannedSources, func(i, j int) bool {
		return sourceAuthorityKey(plannedSources[i]) < sourceAuthorityKey(plannedSources[j])
	})
	sort.Slice(observedSources, func(i, j int) bool {
		return sourceAuthorityKey(observedSources[i]) < sourceAuthorityKey(observedSources[j])
	})
	plannedJSON, _ := json.Marshal(plannedSources)
	observedJSON, _ := json.Marshal(observedSources)
	if string(plannedJSON) != string(observedJSON) {
		return ApplyResult{PlanDigest: plan.PlanDigest}, errors.New("source SPEC representation drift: frozen authorities differ from exact re-observation")
	}
	files, err := filecas.ApplyFileMutations(root, plan.Files)
	result := ApplyResult{OK: err == nil && files.OK, PlanDigest: plan.PlanDigest, Files: files}
	if err != nil {
		return result, err
	}
	return result, nil
}
