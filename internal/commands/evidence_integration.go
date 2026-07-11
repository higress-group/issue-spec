package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/workflow"
)

type nativeEvidenceProvider interface {
	ResolveTarget(context.Context, string, int, string) (coreevidence.NativeTarget, error)
	UpsertArchiveReference(context.Context, coreevidence.NativeTarget, codereview.Reference, string, string, string) error
}

func defaultNewNativeEvidenceProvider(profile auth.Profile, token string) (nativeEvidenceProvider, error) {
	return coreevidence.NewNativeProvider(profile, token)
}

type externalEvidenceConsumption struct {
	ProviderKey        string   `json:"provider_key"`
	ExternalRepository string   `json:"external_repository"`
	ChangeID           string   `json:"change_id"`
	SubjectRevision    string   `json:"subject_revision"`
	EvidenceIDs        []string `json:"evidence_ids"`
}

type externalGateResult struct {
	Consumption externalEvidenceConsumption `json:"consumption"`
	Evaluation  coreevidence.Result         `json:"evaluation"`
	Snapshot    codereview.Snapshot         `json:"-"`
	Target      coreevidence.NativeTarget   `json:"-"`
	Native      nativeEvidenceProvider      `json:"-"`
}

func (a *app) externalGate(ctx context.Context, host, token, repo string, issue int, relationKind,
	expectedRevision string, gate coreevidence.Gate) (externalGateResult, bool, error) {
	profile, _, err := auth.ResolveProfile(a.profileName, host)
	if err != nil {
		return externalGateResult{}, false, err
	}
	if profile.Kind != auth.ProfileKindHosted {
		return externalGateResult{}, false, nil
	}
	if a.newNativeEvidenceProvider == nil {
		return externalGateResult{}, true, errors.New("self-hosted native evidence provider is unavailable")
	}
	native, err := a.newNativeEvidenceProvider(profile, token)
	if err != nil {
		return externalGateResult{}, true, err
	}
	target, err := native.ResolveTarget(ctx, repo, issue, relationKind)
	if err != nil {
		return externalGateResult{}, true, err
	}
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision != "" && expectedRevision != target.SubjectRevision {
		return externalGateResult{}, true, fmt.Errorf("external %s revision mismatch: reference is %s, command requested %s",
			relationKind, target.SubjectRevision, expectedRevision)
	}
	plan, err := workflow.Resolve(".")
	if err != nil {
		return externalGateResult{}, true, fmt.Errorf("resolve workflow evidence policy: %w", err)
	}
	if config := plan.Config.ExternalCode; config != nil && strings.TrimSpace(config.ProviderKey) != target.Reference.ProviderKey {
		return externalGateResult{}, true, fmt.Errorf("external code provider mismatch: workflow selects %s, active reference uses %s",
			config.ProviderKey, target.Reference.ProviderKey)
	}
	policy, err := mergedEvidencePolicy(plan.Config.ExternalCode, target.Policy)
	if err != nil {
		return externalGateResult{}, true, err
	}
	request := codereview.SnapshotRequest{Reference: target.Reference, SubjectRevision: target.SubjectRevision}
	snapshot, err := codereview.FetchSnapshot(ctx, target.Provider, request)
	if err != nil {
		return externalGateResult{}, true, fmt.Errorf("fetch external evidence snapshot: %w", err)
	}
	evaluation := coreevidence.Evaluate(snapshot, policy, coreevidence.Target{Gate: gate,
		Reference: target.Reference, SubjectRevision: target.SubjectRevision, Now: time.Now().UTC()})
	result := externalGateResult{Evaluation: evaluation, Snapshot: snapshot, Target: target, Native: native,
		Consumption: externalEvidenceConsumption{ProviderKey: target.Reference.ProviderKey,
			ExternalRepository: target.Reference.ExternalRepository, ChangeID: target.Reference.ChangeID,
			SubjectRevision: target.SubjectRevision, EvidenceIDs: append([]string(nil), evaluation.EvidenceIDs...)}}
	if !evaluation.Passed {
		return result, true, externalGateFailure(relationKind, evaluation)
	}
	return result, true, nil
}

func (a *app) externalMutationTarget(ctx context.Context, host, token, repo string, issue int,
	relationKind, expectedRevision string, capability codereview.Capability) (coreevidence.NativeTarget, codereview.MutationProvider, nativeEvidenceProvider, bool, error) {
	profile, _, err := auth.ResolveProfile(a.profileName, host)
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, false, err
	}
	if profile.Kind != auth.ProfileKindHosted {
		return coreevidence.NativeTarget{}, nil, nil, false, nil
	}
	native, err := a.newNativeEvidenceProvider(profile, token)
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	target, err := native.ResolveTarget(ctx, repo, issue, relationKind)
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	if expected := strings.TrimSpace(expectedRevision); expected != "" && expected != target.SubjectRevision {
		return coreevidence.NativeTarget{}, nil, nil, true, fmt.Errorf("external %s revision mismatch: reference is %s, command requested %s",
			relationKind, target.SubjectRevision, expected)
	}
	plan, err := workflow.Resolve(".")
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	if plan.Config.ExternalCode != nil && plan.Config.ExternalCode.ProviderKey != target.Reference.ProviderKey {
		return coreevidence.NativeTarget{}, nil, nil, true, fmt.Errorf("external code provider mismatch: workflow selects %s, active reference uses %s",
			plan.Config.ExternalCode.ProviderKey, target.Reference.ProviderKey)
	}
	if a.resolveCodeMutationProvider == nil {
		return coreevidence.NativeTarget{}, nil, nil, true, codereview.ErrProviderNotFound
	}
	provider, err := a.resolveCodeMutationProvider(ctx, target.Reference.ProviderKey)
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	if _, err := codereview.RequireCapabilities(ctx, provider, capability); err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	return target, provider, native, true, nil
}

func mergedEvidencePolicy(config *workflow.ExternalCodeConfig, native coreevidence.NativePolicy) (coreevidence.Policy, error) {
	policy := coreevidence.Policy{Freshness: map[codereview.EvidenceKind]time.Duration{},
		BlockingReviewSeverities: []string{"P0", "P1"}}
	for _, requirement := range native.Requirements {
		policy.RequiredKinds = append(policy.RequiredKinds, requirement.Kind)
		if requirement.Freshness > 0 {
			policy.Freshness[requirement.Kind] = requirement.Freshness
		}
	}
	if config != nil {
		for _, raw := range config.Evidence.Required {
			kind := codereview.EvidenceKind(strings.TrimSpace(raw))
			if !commandEvidenceKind(kind) {
				return coreevidence.Policy{}, fmt.Errorf("workflow requires unsupported evidence kind %q", raw)
			}
			policy.RequiredKinds = append(policy.RequiredKinds, kind)
		}
		policy.RequiredChecks = append(policy.RequiredChecks, config.Evidence.RequiredChecks...)
		for rawKind, rawDuration := range config.Evidence.Freshness {
			kind := codereview.EvidenceKind(strings.TrimSpace(rawKind))
			duration, err := time.ParseDuration(rawDuration)
			if !commandEvidenceKind(kind) || err != nil || duration <= 0 {
				return coreevidence.Policy{}, fmt.Errorf("workflow evidence freshness %s is invalid", rawKind)
			}
			if current := policy.Freshness[kind]; current == 0 || duration < current {
				policy.Freshness[kind] = duration
			}
		}
	}
	policy.RequiredKinds = dedupeEvidenceKinds(policy.RequiredKinds)
	policy.RequiredChecks = dedupeTrimmed(policy.RequiredChecks)
	return policy, nil
}

func externalGateFailure(relation string, result coreevidence.Result) error {
	parts := make([]string, 0, len(result.Failures))
	for _, failure := range result.Failures {
		message := failure.Code + ": " + failure.Message
		if failure.EvidenceID != "" {
			message += " [" + failure.EvidenceID + "]"
		}
		parts = append(parts, message)
	}
	return fmt.Errorf("external %s evidence gate failed for revision %s: %s", relation,
		result.SubjectRevision, strings.Join(parts, "; "))
}

func commandEvidenceKind(kind codereview.EvidenceKind) bool {
	switch kind {
	case codereview.EvidenceChange, codereview.EvidenceReview, codereview.EvidenceCheck,
		codereview.EvidenceMerge, codereview.EvidenceArchive:
		return true
	default:
		return false
	}
}

func dedupeEvidenceKinds(values []codereview.EvidenceKind) []codereview.EvidenceKind {
	seen := map[codereview.EvidenceKind]bool{}
	result := make([]codereview.EvidenceKind, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func dedupeTrimmed(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
