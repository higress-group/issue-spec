package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/auth"
	changegraph "github.com/higress-group/issue-spec/internal/change"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/reconcile"
)

func (a *app) runWorkflowReconcile(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow reconcile", a.err)
	planPath := fs.String("plan", "", "versioned reconcile plan JSON file, or - for stdin")
	projectionPath := fs.String("projection", "", "versioned accepted-receipt projection JSON file, or - for stdin")
	checkpoint := fs.String("checkpoint", "", "durable checkpoint JSON path")
	host := fs.String("hostname", "", "issue backend hostname override")
	allowNonAtomic := fs.Bool("allow-nonatomic", false, "allow guarded non-atomic fallback for an accepted-receipt projection")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	planProvided, projectionProvided := strings.TrimSpace(*planPath) != "", strings.TrimSpace(*projectionPath) != ""
	if planProvided == projectionProvided {
		a.errorf("exactly one of --plan or --projection is required\n")
		return 2
	}
	if planProvided && *allowNonAtomic {
		a.errorf("--allow-nonatomic is only valid with --projection; plan files declare allow_nonatomic directly\n")
		return 2
	}
	var plan reconcile.Plan
	var projection *reconcile.ReceiptProjection
	var err error
	if projectionProvided {
		var value reconcile.ReceiptProjection
		value, err = readReceiptProjection(*projectionPath, a.in)
		if err == nil {
			value.AllowNonAtomic = value.AllowNonAtomic || *allowNonAtomic
			projection = &value
			plan, err = reconcile.CompileReceiptProjection(value)
		}
		if err != nil {
			a.errorf("read receipt projection: %v\n", err)
			return 2
		}
	} else {
		plan, err = readReconcilePlan(*planPath, a.in)
		if err != nil {
			a.errorf("read reconcile plan: %v\n", err)
			return 2
		}
	}
	if _, ok := a.validateRepo(plan.Repo); !ok {
		return 2
	}
	if *host == "" {
		*host = plan.Hostname
	}
	if *host == "" {
		*host = "github.com"
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for workflow reconcile on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	var located changegraph.Located
	if plan.Proposal > 0 {
		located, err = changegraph.Locate(ctx, client, plan.Repo, plan.Proposal)
		if err != nil {
			a.errorf("locate change: %v\n", err)
			return 1
		}
		if projection != nil && projection.Issue != located.Implement.Number {
			a.errorf("resolve receipt projection: proposal change %q resolves implement issue %d, projection targets issue %d\n",
				located.Change, located.Implement.Number, projection.Issue)
			return 2
		}
		if err := resolvePlanRoles(&plan, located); err != nil {
			a.errorf("resolve plan target: %v\n", err)
			return 2
		}
		if projectionProvided {
			if err := resolveReceiptRelationshipAuthority(ctx, client, &plan, located); err != nil {
				a.errorf("resolve receipt relationship authority: %v\n", err)
				return 2
			}
			plan.PlanDigest = ""
			if _, digest, validateErr := reconcile.Validate(plan); validateErr != nil {
				a.errorf("validate resolved receipt projection: %v\n", validateErr)
				return 2
			} else {
				plan.PlanDigest = digest
			}
		}
	} else if hasPlanRoles(plan) {
		a.errorf("plan targets use roles but proposal is absent\n")
		return 2
	}
	result, err := (reconcile.Engine{Backend: client}).Run(ctx, plan, *checkpoint)
	if located.Change != "" {
		result.Change = located
	}
	if err != nil {
		a.errorf("workflow reconcile: %v\n", err)
		return 1
	}
	if *jsonOut {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(a.out, "reconcile %s: created=%d updated=%d unchanged=%d conflicted=%d pending=%d atomic=%v\n", result.PlanDigest, result.Created, result.Updated, result.Unchanged, result.Conflicted, result.Pending, result.Atomic)
	}
	if !result.OK {
		return 1
	}
	return 0
}

func readReceiptProjection(path string, stdin io.Reader) (reconcile.ReceiptProjection, error) {
	reader, closeReader, err := reconcileInput(path, stdin)
	if err != nil {
		return reconcile.ReceiptProjection{}, err
	}
	if closeReader != nil {
		defer closeReader.Close()
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var projection reconcile.ReceiptProjection
	if err := decoder.Decode(&projection); err != nil {
		return projection, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return projection, fmt.Errorf("projection contains trailing JSON")
	}
	return projection, nil
}

func reconcileInput(path string, stdin io.Reader) (io.Reader, io.Closer, error) {
	if path == "-" {
		return stdin, nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return file, file, nil
}

func readReconcilePlan(path string, stdin io.Reader) (reconcile.Plan, error) {
	var reader io.Reader
	base := "."
	if path == "-" {
		reader = stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return reconcile.Plan{}, err
		}
		defer file.Close()
		reader, base = file, filepath.Dir(path)
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var plan reconcile.Plan
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return plan, fmt.Errorf("plan contains trailing JSON")
	}
	for i := range plan.Operations {
		desired := &plan.Operations[i].Desired
		if desired.BodyFile == "" {
			continue
		}
		if desired.Body != "" {
			return plan, fmt.Errorf("operation %s declares both body and body_file", plan.Operations[i].ID)
		}
		bodyPath := desired.BodyFile
		if !filepath.IsAbs(bodyPath) {
			bodyPath = filepath.Join(base, bodyPath)
		}
		data, err := os.ReadFile(bodyPath)
		if err != nil {
			return plan, fmt.Errorf("operation %s body_file: %w", plan.Operations[i].ID, err)
		}
		desired.Body, desired.BodyFile = string(data), ""
	}
	return plan, nil
}

func hasPlanRoles(plan reconcile.Plan) bool {
	for _, op := range plan.Operations {
		if op.Target.Role != "" || (op.Desired.Peer != nil && op.Desired.Peer.Role != "") {
			return true
		}
	}
	return false
}
func resolvePlanRoles(plan *reconcile.Plan, located changegraph.Located) error {
	resolve := func(target *reconcile.Target) error {
		if target.Role == "" {
			return nil
		}
		switch strings.ToLower(target.Role) {
		case "proposal":
			target.Issue = located.Proposal.Number
		case "design":
			target.Issue = located.Design.Number
		case "implement":
			target.Issue = located.Implement.Number
		default:
			return fmt.Errorf("unknown role %q", target.Role)
		}
		target.Role = ""
		return nil
	}
	for i := range plan.Operations {
		if err := resolve(&plan.Operations[i].Target); err != nil {
			return err
		}
		if plan.Operations[i].Desired.Peer != nil {
			if err := resolve(plan.Operations[i].Desired.Peer); err != nil {
				return err
			}
		}
	}
	return nil
}

type resolvedRelationshipArtifact struct {
	target  reconcile.Target
	comment github.Comment
}

func resolveReceiptRelationshipAuthority(ctx context.Context, backend github.IssueBackend, plan *reconcile.Plan,
	located changegraph.Located) error {
	cache := map[int][]github.Comment{}
	comments := func(issue int) ([]github.Comment, error) {
		if value, ok := cache[issue]; ok {
			return value, nil
		}
		value, err := backend.ListIssueComments(ctx, plan.Repo, issue)
		if err != nil {
			return nil, err
		}
		cache[issue] = value
		return value, nil
	}
	observe := func(target reconcile.Target) (resolvedRelationshipArtifact, error) {
		wantIssue, err := canonicalRelationshipIssue(target.Type, located)
		if err != nil {
			return resolvedRelationshipArtifact{}, err
		}
		if target.Issue != wantIssue {
			return resolvedRelationshipArtifact{}, fmt.Errorf("%s %s targets issue %d outside canonical change issue %d",
				target.Type, target.ID, target.Issue, wantIssue)
		}
		items, err := comments(wantIssue)
		if err != nil {
			return resolvedRelationshipArtifact{}, fmt.Errorf("list issue %d comments: %w", wantIssue, err)
		}
		var matches []github.Comment
		for _, item := range items {
			typed := model.ParseTypedComment(item.Body)
			if typed.Type == strings.ToUpper(target.Type) && typed.ID == target.ID {
				matches = append(matches, item)
			}
		}
		if len(matches) != 1 {
			return resolvedRelationshipArtifact{}, fmt.Errorf("%s %s has %d canonical provider carriers on issue %d",
				target.Type, target.ID, len(matches), wantIssue)
		}
		if typed := model.ParseTypedComment(matches[0].Body); len(typed.Errors) != 0 {
			return resolvedRelationshipArtifact{}, fmt.Errorf("%s %s provider carrier is not one canonical typed artifact",
				target.Type, target.ID)
		}
		observedIssue, err := github.ParseIssueNumber(matches[0].HTMLURL)
		if err != nil || observedIssue != wantIssue || strings.TrimSpace(matches[0].HTMLURL) == "" {
			return resolvedRelationshipArtifact{}, fmt.Errorf("%s %s provider URL does not belong to canonical issue %d",
				target.Type, target.ID, wantIssue)
		}
		return resolvedRelationshipArtifact{target: target, comment: matches[0]}, nil
	}

	for index := range plan.Operations {
		op := &plan.Operations[index]
		if op.Kind != "link" || !op.Desired.CarrierAuthorizedBacklink {
			continue
		}
		carrier, err := observe(op.Target)
		if err != nil {
			return fmt.Errorf("operation %s carrier: %w", op.ID, err)
		}
		peer, err := observe(*op.Desired.Peer)
		if err != nil {
			return fmt.Errorf("operation %s peer: %w", op.ID, err)
		}
		expected := op.Precondition.AcceptedReceipt
		observedReceipt, found, err := model.ObserveAcceptedReceiptAuthority(carrier.comment.Body, expected.Role)
		if err != nil || !found || observedReceipt.ReceiptID != expected.ReceiptID || observedReceipt.Digest != expected.Digest ||
			observedReceipt.Generation != expected.Generation {
			return fmt.Errorf("operation %s carrier accepted receipt identity is stale or mismatched", op.ID)
		}
		carrierTyped := model.ParseTypedComment(carrier.comment.Body)
		if len(carrierTyped.Errors) != 0 || carrierTyped.Status != "done" {
			return fmt.Errorf("operation %s carrier must be one canonical immutable done artifact", op.ID)
		}
		assignmentProcess, binding, err := resolveRelationshipAssignmentProcess(comments, located.Implement.Number,
			expected.Role, carrier, observedReceipt)
		if err != nil {
			return fmt.Errorf("operation %s: %w", op.ID, err)
		}
		authorized, err := acceptedRelationshipTargetIDs(expected.Role, carrier.comment.Body, assignmentProcess.comment.Body)
		if err != nil {
			return fmt.Errorf("operation %s: %w", op.ID, err)
		}
		peerKey := strings.ToUpper(peer.target.Type) + ":" + peer.target.ID
		if !authorized[peerKey] {
			return fmt.Errorf("operation %s: accepted %s authority does not cover relationship target %s",
				op.ID, expected.Role, peerKey)
		}
		expected.AssignmentID, expected.AssignmentDigest = binding.AssignmentID, binding.Digest
		op.Precondition.RelationshipAuthority = &reconcile.RelationshipAuthority{
			CarrierURL: carrier.comment.HTMLURL, CarrierBodyDigest: model.RepresentationDigest(carrier.comment.Body),
			PeerURL: peer.comment.HTMLURL, AssignmentProcess: &assignmentProcess.target,
			AssignmentProcessURL:        assignmentProcess.comment.HTMLURL,
			AssignmentProcessBodyDigest: model.RepresentationDigest(assignmentProcess.comment.Body),
			AssignmentID:                binding.AssignmentID, AssignmentDigest: binding.Digest, AssignmentGeneration: binding.Generation,
		}
	}
	return nil
}

func canonicalRelationshipIssue(artifactType string, located changegraph.Located) (int, error) {
	switch strings.ToUpper(strings.TrimSpace(artifactType)) {
	case "SPEC":
		return located.Proposal.Number, nil
	case "TASK":
		return located.Design.Number, nil
	case "PROCESS", "REVIEW", "VERIFY":
		return located.Implement.Number, nil
	default:
		return 0, fmt.Errorf("unsupported relationship artifact type %q", artifactType)
	}
}

func resolveRelationshipAssignmentProcess(comments func(int) ([]github.Comment, error), implementIssue int,
	role assignment.Role, carrier resolvedRelationshipArtifact,
	receipt model.AcceptedReceiptAuthority) (resolvedRelationshipArtifact, *processworkspace.AssignmentBinding, error) {
	if role == assignment.RoleImplementation {
		workspace := model.ParseProcessWorkspace(carrier.target.ID, carrier.comment.HTMLURL, carrier.comment.Body)
		if !workspace.Explicit || workspace.Blocking() || workspace.Workspace == nil || workspace.Workspace.Assignment == nil {
			return resolvedRelationshipArtifact{}, nil, fmt.Errorf("implementation carrier lacks canonical assignment authority")
		}
		binding := workspace.Workspace.Assignment
		if binding.Role != role || binding.Generation != receipt.Generation {
			return resolvedRelationshipArtifact{}, nil, fmt.Errorf("implementation carrier assignment authority does not match accepted receipt")
		}
		return carrier, binding, nil
	}
	items, err := comments(implementIssue)
	if err != nil {
		return resolvedRelationshipArtifact{}, nil, err
	}
	var matches []resolvedRelationshipArtifact
	var bindings []*processworkspace.AssignmentBinding
	for _, item := range items {
		typed := model.ParseTypedComment(item.Body)
		if typed.Type != "PROCESS" || len(typed.Errors) != 0 {
			continue
		}
		workspace := model.ParseProcessWorkspace(typed.ID, item.HTMLURL, item.Body)
		if !workspace.Explicit || workspace.Blocking() || workspace.Workspace == nil || workspace.Workspace.Assignment == nil {
			continue
		}
		binding := workspace.Workspace.Assignment
		if binding.Role == role && binding.AssignmentID == receipt.AssignmentID && binding.Digest == receipt.AssignmentDigest &&
			binding.Generation == receipt.Generation {
			observedIssue, urlErr := github.ParseIssueNumber(item.HTMLURL)
			if urlErr != nil || observedIssue != implementIssue || strings.TrimSpace(item.HTMLURL) == "" {
				return resolvedRelationshipArtifact{}, nil, fmt.Errorf("assignment PROCESS provider URL does not belong to canonical implement issue %d", implementIssue)
			}
			matches = append(matches, resolvedRelationshipArtifact{target: reconcile.Target{Issue: implementIssue, Type: "PROCESS", ID: typed.ID}, comment: item})
			bindings = append(bindings, binding)
		}
	}
	if len(matches) != 1 {
		return resolvedRelationshipArtifact{}, nil, fmt.Errorf("accepted %s assignment has %d canonical PROCESS authorities", role, len(matches))
	}
	return matches[0], bindings[0], nil
}

func acceptedRelationshipTargetIDs(role assignment.Role, carrierBody, assignmentProcessBody string) (map[string]bool, error) {
	result := map[string]bool{}
	process := model.ParseTypedComment(assignmentProcessBody)
	if process.Type != "PROCESS" || len(process.Errors) != 0 {
		return nil, fmt.Errorf("assignment PROCESS authority is not canonical")
	}
	result["PROCESS:"+process.ID] = true
	if process.Assignment != nil {
		for _, scenario := range process.Assignment.ScenarioSelectors {
			if err := model.ValidateTypedIdentity("SPEC", scenario.SpecID); err != nil {
				return nil, fmt.Errorf("assignment PROCESS scenario authority: %w", err)
			}
			result["SPEC:"+scenario.SpecID] = true
		}
	}
	for _, heading := range []string{"### Parent TASK", "### Covers", "### Dependencies"} {
		values, err := processSectionList(assignmentProcessBody, heading)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			artifactType := strings.SplitN(value, "-", 2)[0]
			if artifactType != "SPEC" && artifactType != "TASK" && artifactType != "PROCESS" {
				continue
			}
			if err := model.ValidateTypedIdentity(artifactType, value); err != nil {
				return nil, err
			}
			result[artifactType+":"+value] = true
		}
	}
	if role == assignment.RoleVerification {
		values, err := processSectionList(carrierBody, "### Covered SPECs")
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			if err := model.ValidateTypedIdentity("SPEC", value); err != nil {
				return nil, fmt.Errorf("verification SpecRef authority: %w", err)
			}
			result["SPEC:"+value] = true
		}
	}
	return result, nil
}
