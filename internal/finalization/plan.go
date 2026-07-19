package finalization

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/reconcile"
)

const (
	IntentVersion = 1
	PlanVersion   = 1
)

var (
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Intent is the only authority for creating supersession relationships.
type Intent struct {
	Version          int          `json:"version"`
	BaselineRevision string       `json:"baseline_revision"`
	SupersededBy     []IntentEdge `json:"superseded_by"`
}

type IntentEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Observation is one exact provider representation used to compile a plan.
// Body is intentionally not serialized into the frozen representation list;
// mutation bodies live only in the embedded reconcile plan where needed.
type Observation struct {
	Issue                 int
	CommentID             int64
	URL                   string
	APIURL                string
	Body                  string
	RepresentationVersion int64
}

type Representation struct {
	Issue                 int    `json:"issue"`
	CommentID             int64  `json:"comment_id"`
	URL                   string `json:"url"`
	APIURL                string `json:"api_url,omitempty"`
	Type                  string `json:"type,omitempty"`
	ID                    string `json:"id,omitempty"`
	RepresentationVersion int64  `json:"representation_version,omitempty"`
	RepresentationDigest  string `json:"representation_digest"`
}

type Blocker struct {
	Code       string `json:"code"`
	ArtifactID string `json:"artifact_id,omitempty"`
	Message    string `json:"message"`
}

type Subject struct {
	PullRequest            int    `json:"pull_request"`
	URL                    string `json:"url"`
	SubjectRevision        string `json:"subject_revision"`
	ProviderBaseRevision   string `json:"provider_base_revision"`
	BaselineRevision       string `json:"baseline_revision"`
	ProviderEvidenceDigest string `json:"provider_evidence_digest"`
}

// Plan is a frozen provider snapshot plus the only ordered mutation plan apply
// may execute.
type Plan struct {
	Version         int              `json:"version"`
	PlanDigest      string           `json:"plan_digest,omitempty"`
	Repository      string           `json:"repository"`
	Hostname        string           `json:"hostname,omitempty"`
	Proposal        int              `json:"proposal"`
	Design          int              `json:"design"`
	Implement       int              `json:"implement"`
	Subject         Subject          `json:"subject"`
	Intent          Intent           `json:"intent"`
	IntentDigest    string           `json:"intent_digest"`
	GraphDigest     string           `json:"graph_digest"`
	Selection       SelectionSummary `json:"selection"`
	Representations []Representation `json:"representations"`
	Blockers        []Blocker        `json:"blockers,omitempty"`
	Reconcile       reconcile.Plan   `json:"reconcile"`
}

// SelectionSummary omits comment bodies while retaining all chain identities
// needed by detail and apply's final re-observation.
type SelectionSummary struct {
	Edges            []SupersessionEdge  `json:"edges"`
	Historical       []HistoricalProcess `json:"historical"`
	ActiveProcessIDs []string            `json:"active_process_ids"`
}

type CompileInput struct {
	Repository      string
	Hostname        string
	Proposal        int
	Design          int
	Implement       int
	Subject         Subject
	Intent          Intent
	Observations    []Observation
	LifecycleReady  bool
	LifecycleBlocks []Blocker
}

// ReadIntent strictly decodes one versioned intent document.
func ReadIntent(r io.Reader) (Intent, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var intent Intent
	if err := decoder.Decode(&intent); err != nil {
		return intent, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return intent, err
	}
	_, err := normalizeIntent(intent)
	return intent, err
}

// ReadPlan strictly decodes and validates a frozen plan.
func ReadPlan(r io.Reader) (Plan, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return plan, err
	}
	if err := ValidatePlan(plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("document contains trailing JSON")
		}
		return err
	}
	return nil
}

// ProjectIntent preserves the original helper for callers whose snapshot has
// exactly one PROCESS-owning issue. Finalization commands should use
// ProjectIntentForImplement so the provider Implement identity is explicit.
func ProjectIntent(intent Intent, observations []Observation) ([]model.Artifact, Selection, error) {
	implement, err := soleProcessIssue(observations)
	if err != nil {
		return nil, Selection{}, err
	}
	return ProjectIntentForImplement(intent, implement, observations)
}

// ProjectIntentForImplement validates explicit edges against the exact
// observed Implement issue, stamps them in memory, and computes the shared
// selection without writes. PROCESS artifacts on another issue cannot become
// replacement endpoints or active carriers for this change.
func ProjectIntentForImplement(intent Intent, implement int, observations []Observation) ([]model.Artifact, Selection, error) {
	intent, err := normalizeIntent(intent)
	if err != nil {
		return nil, Selection{}, err
	}
	if implement <= 0 {
		return nil, Selection{}, errors.New("Implement issue identity is required")
	}
	artifacts := artifactsFromObservations(observations)
	byID := make(map[string]int)
	for i, artifact := range artifacts {
		if artifact.Comment.Type != "PROCESS" || artifact.Issue != implement {
			continue
		}
		if _, exists := byID[artifact.Comment.ID]; exists {
			return nil, Selection{}, fmt.Errorf("duplicate PROCESS identity %s", artifact.Comment.ID)
		}
		byID[artifact.Comment.ID] = i
	}
	for _, edge := range intent.SupersededBy {
		from, fromOK := byID[edge.From]
		to, toOK := byID[edge.To]
		if !fromOK || !toOK {
			return nil, Selection{}, fmt.Errorf("superseded-by edge %s -> %s requires both PROCESS identities on the Implement issue", edge.From, edge.To)
		}
		target := artifacts[to]
		if strings.TrimSpace(target.URL) == "" {
			return nil, Selection{}, fmt.Errorf("superseded-by target %s has no exact provider URL", edge.To)
		}
		body, _, stampErr := model.StampSupersededBy(artifacts[from].Comment.Body, edge.From,
			model.SupersededBy{ProcessID: edge.To, URL: target.URL})
		if stampErr != nil {
			return nil, Selection{}, fmt.Errorf("superseded-by edge %s -> %s: %w", edge.From, edge.To, stampErr)
		}
		artifacts[from].Comment = model.ParseTypedComment(body)
	}
	selection := EvaluateSelection(selectionArtifactsForImplement(artifacts, implement))
	if !selection.Valid() {
		return artifacts, selection, nil
	}
	return artifacts, selection, nil
}

// ProjectLifecycle applies only the in-memory historical superseded statuses
// used by the existing evidence evaluator. It never changes active carriers.
func ProjectLifecycle(artifacts []model.Artifact, selection Selection) ([]model.Artifact, error) {
	result := append([]model.Artifact(nil), artifacts...)
	historical := make(map[string]bool, len(selection.Historical))
	for _, item := range selection.Historical {
		historical[item.ProcessID] = true
	}
	for i := range result {
		if !historical[result[i].Comment.ID] || result[i].Comment.Type != "PROCESS" {
			continue
		}
		transition, err := model.ApplyTypedTransition(result[i].Comment.Body, model.TransitionRequest{
			ExpectedType: "PROCESS", ExpectedID: result[i].Comment.ID, ToStatus: "superseded",
		})
		if err != nil {
			return nil, fmt.Errorf("project historical PROCESS %s: %w", result[i].Comment.ID, err)
		}
		result[i].Comment = model.ParseTypedComment(transition.Body)
	}
	return result, nil
}

func Compile(input CompileInput) (Plan, error) {
	intent, err := normalizeIntent(input.Intent)
	if err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(input.Repository) == "" || input.Proposal <= 0 || input.Design <= 0 || input.Implement <= 0 {
		return Plan{}, errors.New("repository and Proposal/Design/Implement identities are required")
	}
	input.Subject.URL = strings.TrimSpace(input.Subject.URL)
	input.Subject.SubjectRevision = strings.TrimSpace(input.Subject.SubjectRevision)
	input.Subject.ProviderBaseRevision = strings.TrimSpace(input.Subject.ProviderBaseRevision)
	input.Subject.BaselineRevision = strings.TrimSpace(input.Subject.BaselineRevision)
	input.Subject.ProviderEvidenceDigest = strings.TrimSpace(input.Subject.ProviderEvidenceDigest)
	if input.Subject.PullRequest <= 0 || input.Subject.URL == "" ||
		!validRevision(input.Subject.SubjectRevision) || !validRevision(input.Subject.ProviderBaseRevision) ||
		!validRevision(input.Subject.BaselineRevision) ||
		!digestPattern.MatchString(input.Subject.ProviderEvidenceDigest) {
		return Plan{}, errors.New("exact pull request subject and baseline identities are required")
	}
	if intent.BaselineRevision != input.Subject.BaselineRevision {
		return Plan{}, fmt.Errorf("intent baseline %s differs from actual baseline %s", intent.BaselineRevision, input.Subject.BaselineRevision)
	}
	observations, err := normalizeObservations(input.Observations)
	if err != nil {
		return Plan{}, err
	}
	projected, selection, err := ProjectIntentForImplement(intent, input.Implement, observations)
	if err != nil {
		return Plan{}, err
	}
	representations := representationsOf(observations)
	blockers := append([]Blocker(nil), input.LifecycleBlocks...)
	for _, diagnostic := range selection.Diagnostics {
		blockers = append(blockers, Blocker{Code: diagnostic.Code, ArtifactID: diagnostic.ProcessID, Message: diagnostic.Message})
	}
	for _, id := range selection.LegacySupersededProcessIDs {
		blockers = append(blockers, Blocker{Code: "legacy-superseded-without-authority", ArtifactID: id,
			Message: "Status: superseded without an explicit superseded-by relationship remains active and blocking"})
	}
	blockers = normalizeBlockers(blockers)
	selectionSummary := summarizeSelection(selection)
	graphDigest, err := digestJSON(selectionSummary)
	if err != nil {
		return Plan{}, err
	}
	intentDigest, err := digestJSON(intent)
	if err != nil {
		return Plan{}, err
	}
	reconcilePlan := reconcile.Plan{Version: reconcile.PlanVersion, Repo: strings.TrimSpace(input.Repository),
		Hostname: strings.TrimSpace(input.Hostname), Proposal: input.Proposal, Operations: []reconcile.Operation{}}
	if selection.Valid() {
		reconcilePlan, err = compileReconcile(input, intent, observations, projected, selection, input.LifecycleReady && len(blockers) == 0)
	}
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Version: PlanVersion, Repository: strings.TrimSpace(input.Repository), Hostname: strings.TrimSpace(input.Hostname),
		Proposal: input.Proposal, Design: input.Design, Implement: input.Implement, Subject: input.Subject, Intent: intent,
		IntentDigest: intentDigest, GraphDigest: graphDigest, Selection: selectionSummary,
		Representations: representations, Blockers: blockers, Reconcile: reconcilePlan}
	digest, err := DigestPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.PlanDigest = digest
	return plan, ValidatePlan(plan)
}

func compileReconcile(input CompileInput, intent Intent, observations []Observation, projected []model.Artifact,
	selection Selection, lifecycleReady bool) (reconcile.Plan, error) {
	plan := reconcile.Plan{Version: reconcile.PlanVersion, Repo: strings.TrimSpace(input.Repository), Hostname: strings.TrimSpace(input.Hostname),
		Proposal: input.Proposal, Operations: []reconcile.Operation{}}
	byID := make(map[string]model.Artifact)
	observedByID := make(map[string]Observation)
	byURL := make(map[string]model.Artifact)
	for _, observation := range observations {
		typed := model.ParseTypedComment(observation.Body)
		if typed.ID != "" {
			observedByID[typed.ID] = observation
		}
	}
	plannedBodies := make(map[string]string)
	plannedWrites := make(map[string]bool)
	for _, artifact := range projected {
		if artifact.Comment.ID != "" {
			byID[artifact.Comment.ID] = artifact
			observation, ok := observedByID[artifact.Comment.ID]
			if !ok {
				return reconcile.Plan{}, fmt.Errorf("missing provider observation for %s", artifact.Comment.ID)
			}
			plannedBodies[artifact.Comment.ID] = observation.Body
		}
		for _, raw := range []string{artifact.URL, artifact.APIURL} {
			if normalized := model.NormalizeURL(raw); normalized != "" {
				byURL[normalized] = artifact
			}
		}
	}

	var stampIDs []string
	for _, edge := range intent.SupersededBy {
		artifact := byID[edge.From]
		observation := observedByID[edge.From]
		persisted, found, parseErr := model.ParseSupersededBy(observation.Body, edge.From)
		if parseErr != nil {
			return reconcile.Plan{}, parseErr
		}
		if found && persisted.ProcessID == edge.To && model.NormalizeURL(persisted.URL) == model.NormalizeURL(byID[edge.To].URL) {
			continue
		}
		id := "01-stamp-" + strings.ToLower(edge.From)
		stampIDs = append(stampIDs, id)
		plan.Operations = append(plan.Operations, reconcile.Operation{ID: id, Kind: "upsert",
			Target:       reconcile.Target{Issue: artifact.Issue, Type: "PROCESS", ID: edge.From},
			Desired:      reconcile.Desired{Body: artifact.Comment.Body},
			Precondition: plannedRepresentationPrecondition(observation, plannedBodies[edge.From], plannedWrites[edge.From])})
		plannedBodies[edge.From] = artifact.Comment.Body
		plannedWrites[edge.From] = true
	}

	var linkIDs []string
	for _, edge := range selection.Edges {
		from, to := byID[edge.FromProcessID], byID[edge.ToProcessID]
		fromTarget := reconcile.Target{Issue: from.Issue, Type: "PROCESS", ID: from.Comment.ID}
		toTarget := reconcile.Target{Issue: to.Issue, Type: "PROCESS", ID: to.Comment.ID}
		fromBefore, toBefore := plannedBodies[from.Comment.ID], plannedBodies[to.Comment.ID]
		fromAfter, fromChanged, linkErr := model.AddRelatedCommentLink(fromBefore, to.URL)
		if linkErr != nil {
			return reconcile.Plan{}, fmt.Errorf("plan link %s -> %s: %w", from.Comment.ID, to.Comment.ID, linkErr)
		}
		toAfter, toChanged, linkErr := model.AddRelatedCommentLink(toBefore, from.URL)
		if linkErr != nil {
			return reconcile.Plan{}, fmt.Errorf("plan link %s -> %s: %w", to.Comment.ID, from.Comment.ID, linkErr)
		}
		id := "02-link-" + strings.ToLower(edge.FromProcessID) + "-to-" + strings.ToLower(edge.ToProcessID)
		linkIDs = append(linkIDs, id)
		plan.Operations = append(plan.Operations, reconcile.Operation{ID: id, Kind: "link", DependsOn: append([]string(nil), stampIDs...),
			Target: fromTarget, Desired: reconcile.Desired{Peer: &toTarget},
			Precondition: reconcile.Precondition{Endpoints: []reconcile.EndpointPrecondition{
				plannedEndpointPrecondition(fromTarget, observedByID[from.Comment.ID], fromBefore, fromAfter, plannedWrites[from.Comment.ID]),
				plannedEndpointPrecondition(toTarget, observedByID[to.Comment.ID], toBefore, toAfter, plannedWrites[to.Comment.ID]),
			}}})
		plannedBodies[from.Comment.ID], plannedBodies[to.Comment.ID] = fromAfter, toAfter
		plannedWrites[from.Comment.ID] = plannedWrites[from.Comment.ID] || fromChanged
		plannedWrites[to.Comment.ID] = plannedWrites[to.Comment.ID] || toChanged
	}

	var backlinkIDs []string
	for _, artifact := range projected {
		var role assignment.Role
		switch artifact.Comment.Type {
		case "REVIEW":
			role = assignment.RoleReview
		case "VERIFY":
			role = assignment.RoleVerification
		default:
			continue
		}
		authority, found, authorityErr := model.ObserveAcceptedReceiptAuthority(artifact.Comment.Body, role)
		if authorityErr != nil {
			return reconcile.Plan{}, fmt.Errorf("%s accepted receipt: %w", artifact.Comment.ID, authorityErr)
		}
		if !found || artifact.Comment.Status != "done" {
			continue
		}
		for _, relatedURL := range model.RelatedCommentURLs(artifact.Comment) {
			peer, exists := byURL[model.NormalizeURL(relatedURL)]
			if !exists || (peer.Comment.Type != "PROCESS" && peer.Comment.Type != "SPEC") ||
				hasRelatedIdentity(peer.Comment, artifact.URL, artifact.APIURL) {
				continue
			}
			id := "03-backlink-" + strings.ToLower(artifact.Comment.ID) + "-to-" + strings.ToLower(peer.Comment.ID)
			backlinkIDs = append(backlinkIDs, id)
			carrierTarget := reconcile.Target{Issue: artifact.Issue, Type: artifact.Comment.Type, ID: artifact.Comment.ID}
			peerTarget := reconcile.Target{Issue: peer.Issue, Type: peer.Comment.Type, ID: peer.Comment.ID}
			peerBefore := plannedBodies[peer.Comment.ID]
			peerAfter, peerChanged, linkErr := model.AddRelatedCommentLink(peerBefore, artifact.URL)
			if linkErr != nil {
				return reconcile.Plan{}, fmt.Errorf("plan backlink %s -> %s: %w", peer.Comment.ID, artifact.Comment.ID, linkErr)
			}
			plan.Operations = append(plan.Operations, reconcile.Operation{ID: id, Kind: "link",
				DependsOn: append(append([]string(nil), stampIDs...), linkIDs...),
				Target:    carrierTarget,
				Desired:   reconcile.Desired{Peer: &peerTarget, CarrierAuthorizedBacklink: true},
				Precondition: reconcile.Precondition{AcceptedReceipt: &authority, Endpoints: []reconcile.EndpointPrecondition{
					plannedEndpointPrecondition(peerTarget, observedByID[peer.Comment.ID], peerBefore, peerAfter, plannedWrites[peer.Comment.ID]),
				}}})
			plannedBodies[peer.Comment.ID] = peerAfter
			plannedWrites[peer.Comment.ID] = plannedWrites[peer.Comment.ID] || peerChanged
		}
	}

	prior := append(append(append([]string(nil), stampIDs...), linkIDs...), backlinkIDs...)
	var historicalIDs []string
	for _, historical := range selection.Historical {
		artifact := byID[historical.ProcessID]
		before := plannedBodies[historical.ProcessID]
		transition, transitionErr := model.ApplyTypedTransition(before, model.TransitionRequest{
			ExpectedType: "PROCESS", ExpectedID: historical.ProcessID, ToStatus: "superseded",
		})
		if transitionErr != nil {
			return reconcile.Plan{}, fmt.Errorf("plan historical PROCESS %s: %w", historical.ProcessID, transitionErr)
		}
		id := "04-supersede-" + strings.ToLower(historical.ProcessID)
		historicalIDs = append(historicalIDs, id)
		plan.Operations = append(plan.Operations, reconcile.Operation{ID: id, Kind: "transition", DependsOn: append([]string(nil), prior...),
			Target: reconcile.Target{Issue: artifact.Issue, Type: "PROCESS", ID: historical.ProcessID}, Desired: reconcile.Desired{Status: "superseded"},
			Precondition: plannedRepresentationPrecondition(observedByID[historical.ProcessID], before, plannedWrites[historical.ProcessID])})
		plannedBodies[historical.ProcessID] = transition.Body
		plannedWrites[historical.ProcessID] = plannedWrites[historical.ProcessID] || transition.Changed
	}
	if lifecycleReady {
		processBarrier := append(append([]string(nil), prior...), historicalIDs...)
		var processIDs []string
		for _, idValue := range selection.ActiveProcessIDs {
			artifact := byID[idValue]
			if artifact.Comment.Status == "done" {
				continue
			}
			before := plannedBodies[idValue]
			transition, transitionErr := model.ApplyTypedTransition(before, model.TransitionRequest{
				ExpectedType: "PROCESS", ExpectedID: idValue, ToStatus: "done",
			})
			if transitionErr != nil {
				return reconcile.Plan{}, fmt.Errorf("plan active PROCESS %s: %w", idValue, transitionErr)
			}
			id := "05-complete-" + strings.ToLower(idValue)
			processIDs = append(processIDs, id)
			plan.Operations = append(plan.Operations, reconcile.Operation{ID: id, Kind: "transition", DependsOn: append([]string(nil), processBarrier...),
				Target: reconcile.Target{Issue: artifact.Issue, Type: "PROCESS", ID: idValue}, Desired: reconcile.Desired{Status: "done"},
				Precondition: plannedRepresentationPrecondition(observedByID[idValue], before, plannedWrites[idValue])})
			plannedBodies[idValue] = transition.Body
			plannedWrites[idValue] = plannedWrites[idValue] || transition.Changed
		}
		taskBarrier := append(append([]string(nil), processBarrier...), processIDs...)
		for _, artifact := range projected {
			if artifact.Comment.Type != "TASK" || artifact.Comment.Status == "done" || artifact.Comment.Status == "superseded" {
				continue
			}
			before := plannedBodies[artifact.Comment.ID]
			plan.Operations = append(plan.Operations, reconcile.Operation{ID: "06-complete-" + strings.ToLower(artifact.Comment.ID), Kind: "transition",
				DependsOn: append([]string(nil), taskBarrier...), Target: reconcile.Target{Issue: artifact.Issue, Type: "TASK", ID: artifact.Comment.ID},
				Desired:      reconcile.Desired{Status: "done"},
				Precondition: plannedRepresentationPrecondition(observedByID[artifact.Comment.ID], before, plannedWrites[artifact.Comment.ID])})
		}
	}
	if len(plan.Operations) != 0 {
		_, digest, err := reconcile.Validate(plan)
		if err != nil {
			return reconcile.Plan{}, fmt.Errorf("compile reconcile plan: %w", err)
		}
		plan.PlanDigest = digest
	}
	return plan, nil
}

func ValidatePlan(plan Plan) error {
	if plan.Version != PlanVersion {
		return fmt.Errorf("unsupported finalization plan version %d", plan.Version)
	}
	if strings.TrimSpace(plan.Repository) == "" || plan.Proposal <= 0 || plan.Design <= 0 || plan.Implement <= 0 {
		return errors.New("plan repository and issue identities are incomplete")
	}
	if plan.Subject.PullRequest <= 0 || strings.TrimSpace(plan.Subject.URL) == "" || plan.Subject.URL != strings.TrimSpace(plan.Subject.URL) ||
		!validRevision(plan.Subject.SubjectRevision) || !validRevision(plan.Subject.ProviderBaseRevision) ||
		!validRevision(plan.Subject.BaselineRevision) ||
		!digestPattern.MatchString(plan.Subject.ProviderEvidenceDigest) {
		return errors.New("plan subject or baseline identity is invalid")
	}
	if !digestPattern.MatchString(plan.IntentDigest) {
		return errors.New("plan intent digest is invalid")
	}
	if !digestPattern.MatchString(plan.GraphDigest) {
		return errors.New("plan graph digest is invalid")
	}
	if plan.Reconcile.Repo != plan.Repository || plan.Reconcile.Proposal != plan.Proposal || plan.Reconcile.Version != reconcile.PlanVersion {
		return errors.New("embedded reconcile plan identity differs from finalization plan")
	}
	intent, err := normalizeIntent(plan.Intent)
	if err != nil {
		return fmt.Errorf("plan intent: %w", err)
	}
	if intent.BaselineRevision != plan.Subject.BaselineRevision {
		return errors.New("plan intent baseline differs from frozen subject baseline")
	}
	intentDigest, err := digestJSON(intent)
	rawIntentDigest, rawIntentErr := digestJSON(plan.Intent)
	if err != nil || rawIntentErr != nil || intentDigest != plan.IntentDigest || rawIntentDigest != intentDigest {
		return errors.New("plan intent digest is stale")
	}
	graphDigest, err := digestJSON(plan.Selection)
	if err != nil || graphDigest != plan.GraphDigest {
		return errors.New("plan graph digest is stale")
	}
	if err := validateSelectionSummary(plan.Selection); err != nil {
		return err
	}
	if len(plan.Reconcile.Operations) != 0 {
		ordered, digest, err := reconcile.Validate(plan.Reconcile)
		if err != nil {
			return fmt.Errorf("embedded reconcile plan: %w", err)
		}
		if len(ordered) != len(plan.Reconcile.Operations) {
			return errors.New("embedded reconcile plan order is incomplete")
		}
		for i := range ordered {
			if ordered[i].ID != plan.Reconcile.Operations[i].ID {
				return errors.New("embedded reconcile operations are not in deterministic dependency order")
			}
		}
		if plan.Reconcile.PlanDigest != digest {
			return errors.New("embedded reconcile plan digest is stale")
		}
	}
	if err := validateRepresentations(plan.Representations); err != nil {
		return err
	}
	normalizedBlockers := normalizeBlockers(plan.Blockers)
	rawBlockers, rawBlockersErr := digestJSON(plan.Blockers)
	canonicalBlockers, canonicalBlockersErr := digestJSON(normalizedBlockers)
	if rawBlockersErr != nil || canonicalBlockersErr != nil || rawBlockers != canonicalBlockers {
		return errors.New("plan blockers are not in canonical order")
	}
	digest, err := DigestPlan(plan)
	if err != nil {
		return err
	}
	if plan.PlanDigest != digest {
		return fmt.Errorf("finalization plan digest mismatch: declared=%s computed=%s", plan.PlanDigest, digest)
	}
	return nil
}

func DigestPlan(plan Plan) (string, error) {
	plan.PlanDigest = ""
	return digestJSON(plan)
}

func normalizeIntent(intent Intent) (Intent, error) {
	if intent.Version != IntentVersion {
		return intent, fmt.Errorf("unsupported intent version %d", intent.Version)
	}
	intent.BaselineRevision = strings.TrimSpace(intent.BaselineRevision)
	if !validRevision(intent.BaselineRevision) {
		return intent, errors.New("intent baseline_revision must be a lowercase full 40- or 64-character revision")
	}
	seen := map[string]string{}
	for i := range intent.SupersededBy {
		intent.SupersededBy[i].From = strings.TrimSpace(intent.SupersededBy[i].From)
		intent.SupersededBy[i].To = strings.TrimSpace(intent.SupersededBy[i].To)
		if err := model.ValidateTypedIdentity("PROCESS", intent.SupersededBy[i].From); err != nil {
			return intent, fmt.Errorf("superseded_by[%d].from: %w", i, err)
		}
		if err := model.ValidateTypedIdentity("PROCESS", intent.SupersededBy[i].To); err != nil {
			return intent, fmt.Errorf("superseded_by[%d].to: %w", i, err)
		}
		if intent.SupersededBy[i].From == intent.SupersededBy[i].To {
			return intent, fmt.Errorf("superseded_by[%d] cannot target itself", i)
		}
		if prior, exists := seen[intent.SupersededBy[i].From]; exists {
			if prior == intent.SupersededBy[i].To {
				return intent, fmt.Errorf("duplicate superseded-by edge %s -> %s", intent.SupersededBy[i].From, prior)
			}
			return intent, fmt.Errorf("PROCESS %s has multiple intent successors", intent.SupersededBy[i].From)
		}
		seen[intent.SupersededBy[i].From] = intent.SupersededBy[i].To
	}
	sort.Slice(intent.SupersededBy, func(i, j int) bool {
		if intent.SupersededBy[i].From != intent.SupersededBy[j].From {
			return intent.SupersededBy[i].From < intent.SupersededBy[j].From
		}
		return intent.SupersededBy[i].To < intent.SupersededBy[j].To
	})
	return intent, nil
}

func normalizeObservations(values []Observation) ([]Observation, error) {
	result := append([]Observation(nil), values...)
	seenComment := map[int64]bool{}
	seenTyped := map[string]bool{}
	for i := range result {
		value := &result[i]
		value.URL, value.APIURL = strings.TrimSpace(value.URL), strings.TrimSpace(value.APIURL)
		if value.Issue <= 0 || value.CommentID <= 0 || value.URL == "" || value.Body == "" || value.RepresentationVersion < 0 {
			return nil, fmt.Errorf("observation %d has incomplete provider identity", i)
		}
		if seenComment[value.CommentID] {
			return nil, fmt.Errorf("duplicate provider comment id %d", value.CommentID)
		}
		seenComment[value.CommentID] = true
		typed := model.ParseTypedComment(value.Body)
		if typed.ID != "" {
			key := typed.Type + "/" + typed.ID
			if seenTyped[key] {
				return nil, fmt.Errorf("duplicate typed observation %s", key)
			}
			seenTyped[key] = true
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := model.ParseTypedComment(result[i].Body), model.ParseTypedComment(result[j].Body)
		if result[i].Issue != result[j].Issue {
			return result[i].Issue < result[j].Issue
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		return result[i].CommentID < result[j].CommentID
	})
	return result, nil
}

func artifactsFromObservations(observations []Observation) []model.Artifact {
	artifacts := make([]model.Artifact, 0, len(observations))
	for _, observed := range observations {
		if !model.IsLikelyTyped(observed.Body) && !model.IsLikelyCodeChangeRationale(observed.Body) {
			continue
		}
		artifacts = append(artifacts, model.Artifact{Issue: observed.Issue, CommentID: observed.CommentID,
			URL: observed.URL, APIURL: observed.APIURL, Comment: model.ParseTypedComment(observed.Body)})
	}
	return artifacts
}

func representationsOf(observations []Observation) []Representation {
	result := make([]Representation, 0, len(observations))
	for _, observed := range observations {
		typed := model.ParseTypedComment(observed.Body)
		result = append(result, Representation{Issue: observed.Issue, CommentID: observed.CommentID, URL: observed.URL,
			APIURL: observed.APIURL, Type: typed.Type, ID: typed.ID, RepresentationVersion: observed.RepresentationVersion,
			RepresentationDigest: model.RepresentationDigest(observed.Body)})
	}
	return result
}

func observationPrecondition(observation Observation) reconcile.Precondition {
	if observation.RepresentationVersion > 0 {
		return reconcile.Precondition{RepresentationVersion: observation.RepresentationVersion}
	}
	return reconcile.Precondition{BodyDigest: model.RepresentationDigest(observation.Body)}
}

func plannedRepresentationPrecondition(observation Observation, body string, previouslyWritten bool) reconcile.Precondition {
	if !previouslyWritten && body == observation.Body {
		return observationPrecondition(observation)
	}
	return reconcile.Precondition{BodyDigest: model.RepresentationDigest(body)}
}

func plannedEndpointPrecondition(target reconcile.Target, observation Observation, before, after string,
	previouslyWritten bool) reconcile.EndpointPrecondition {
	result := reconcile.EndpointPrecondition{Target: target, AfterDigest: model.RepresentationDigest(after)}
	if !previouslyWritten && before == observation.Body && observation.RepresentationVersion > 0 {
		result.RepresentationVersion = observation.RepresentationVersion
	} else {
		result.BodyDigest = model.RepresentationDigest(before)
	}
	return result
}

func soleProcessIssue(observations []Observation) (int, error) {
	implement := 0
	for _, observation := range observations {
		if model.ParseTypedComment(observation.Body).Type != "PROCESS" {
			continue
		}
		if implement == 0 {
			implement = observation.Issue
			continue
		}
		if observation.Issue != implement {
			return 0, errors.New("PROCESS observations span multiple issues; exact Implement identity is required")
		}
	}
	if implement == 0 {
		return 0, errors.New("snapshot has no PROCESS on an Implement issue")
	}
	return implement, nil
}

func selectionArtifactsForImplement(artifacts []model.Artifact, implement int) []model.Artifact {
	result := make([]model.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Comment.Type == "PROCESS" && artifact.Issue != implement {
			continue
		}
		result = append(result, artifact)
	}
	return result
}

func summarizeSelection(selection Selection) SelectionSummary {
	return SelectionSummary{Edges: append([]SupersessionEdge(nil), selection.Edges...),
		Historical: append([]HistoricalProcess(nil), selection.Historical...), ActiveProcessIDs: append([]string(nil), selection.ActiveProcessIDs...)}
}

// SummarizeArtifacts re-evaluates the persisted graph for apply's final
// observation without requiring callers to load comment bodies into detail.
func SummarizeArtifacts(artifacts []model.Artifact) (SelectionSummary, string, []Diagnostic, error) {
	implement := 0
	for _, artifact := range artifacts {
		if artifact.Comment.Type != "PROCESS" {
			continue
		}
		if implement == 0 {
			implement = artifact.Issue
			continue
		}
		if artifact.Issue != implement {
			return SelectionSummary{}, "", nil, errors.New("PROCESS artifacts span multiple issues; exact Implement identity is required")
		}
	}
	return SummarizeArtifactsForImplement(artifacts, implement)
}

// SummarizeArtifactsForImplement re-evaluates only PROCESS carriers owned by
// the exact Implement issue while retaining the change's other typed evidence.
func SummarizeArtifactsForImplement(artifacts []model.Artifact, implement int) (SelectionSummary, string, []Diagnostic, error) {
	if implement <= 0 {
		return SelectionSummary{}, "", nil, errors.New("Implement issue identity is required")
	}
	selection := EvaluateSelection(selectionArtifactsForImplement(artifacts, implement))
	summary := summarizeSelection(selection)
	digest, err := digestJSON(summary)
	return summary, digest, append([]Diagnostic(nil), selection.Diagnostics...), err
}

func normalizeBlockers(values []Blocker) []Blocker {
	seen := map[string]bool{}
	result := make([]Blocker, 0, len(values))
	for _, value := range values {
		value.Code, value.ArtifactID, value.Message = strings.TrimSpace(value.Code), strings.TrimSpace(value.ArtifactID), strings.TrimSpace(value.Message)
		if value.Code == "" || value.Message == "" {
			continue
		}
		key := value.Code + "\x00" + value.ArtifactID + "\x00" + value.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Code != result[j].Code {
			return result[i].Code < result[j].Code
		}
		if result[i].ArtifactID != result[j].ArtifactID {
			return result[i].ArtifactID < result[j].ArtifactID
		}
		return result[i].Message < result[j].Message
	})
	return result
}

func hasRelatedIdentity(comment model.TypedComment, identities ...string) bool {
	want := map[string]bool{}
	for _, identity := range identities {
		if normalized := model.NormalizeURL(identity); normalized != "" {
			want[normalized] = true
		}
	}
	for _, related := range model.RelatedCommentURLs(comment) {
		if want[model.NormalizeURL(related)] {
			return true
		}
	}
	return false
}

func validateRepresentations(values []Representation) error {
	if len(values) == 0 {
		return errors.New("plan has no frozen provider representations")
	}
	seen := map[int64]bool{}
	previous := ""
	for _, value := range values {
		if value.Issue <= 0 || value.CommentID <= 0 || strings.TrimSpace(value.URL) == "" ||
			!digestPattern.MatchString(value.RepresentationDigest) || value.RepresentationVersion < 0 {
			return errors.New("plan contains an invalid frozen provider representation")
		}
		if seen[value.CommentID] {
			return fmt.Errorf("plan contains duplicate provider comment id %d", value.CommentID)
		}
		seen[value.CommentID] = true
		key := fmt.Sprintf("%012d\x00%s\x00%s\x00%020d", value.Issue, value.Type, value.ID, value.CommentID)
		if previous != "" && key < previous {
			return errors.New("plan representations are not in canonical order")
		}
		previous = key
	}
	return nil
}

func validateSelectionSummary(summary SelectionSummary) error {
	for i := 1; i < len(summary.Edges); i++ {
		left, right := summary.Edges[i-1], summary.Edges[i]
		if left.FromProcessID > right.FromProcessID ||
			(left.FromProcessID == right.FromProcessID && left.ToProcessID > right.ToProcessID) ||
			(left.FromProcessID == right.FromProcessID && left.ToProcessID == right.ToProcessID && left.TargetURL > right.TargetURL) {
			return errors.New("plan supersession edges are not in canonical order")
		}
	}
	for i, historical := range summary.Historical {
		if i > 0 && summary.Historical[i-1].ProcessID >= historical.ProcessID {
			return errors.New("plan historical chains are not in canonical order")
		}
		if len(historical.Chain) < 2 || historical.Chain[0] != historical.ProcessID ||
			historical.Chain[len(historical.Chain)-1] != historical.ActiveSinkID {
			return fmt.Errorf("plan historical chain %s is invalid", historical.ProcessID)
		}
	}
	for i, id := range summary.ActiveProcessIDs {
		if err := model.ValidateTypedIdentity("PROCESS", id); err != nil {
			return fmt.Errorf("plan active PROCESS: %w", err)
		}
		if i > 0 && summary.ActiveProcessIDs[i-1] >= id {
			return errors.New("plan active PROCESS identities are not in canonical order")
		}
	}
	return nil
}

func validRevision(value string) bool { return revisionPattern.MatchString(strings.TrimSpace(value)) }

func digestJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// CanonicalJSON returns the stable indented sidecar representation.
func CanonicalJSON(plan Plan) ([]byte, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(plan); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
