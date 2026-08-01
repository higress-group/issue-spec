package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/relationships"
	"github.com/higress-group/issue-spec/internal/templates"
)

const (
	acceptedVerificationReceiptStart = "<!-- issue-spec:accepted-verification-receipt version=1 -->"
	acceptedVerificationReceiptEnd   = "<!-- /issue-spec:accepted-verification-receipt -->"
)

// acceptedVerificationReceipt is the compact durable projection of one
// validated direct role-owned receipt plus provider-owned checks observed
// during publication. It contains no evaluator forecast or runtime attestation.
type acceptedVerificationReceipt struct {
	ReceiptID            string                                        `json:"receipt_id"`
	ReceiptDigest        string                                        `json:"receipt_digest"`
	AssignmentID         string                                        `json:"assignment_id"`
	AssignmentDigest     string                                        `json:"assignment_digest"`
	AssignmentGeneration uint64                                        `json:"assignment_generation"`
	SubjectRevision      string                                        `json:"subject_revision"`
	Tests                []acceptedVerificationTest                    `json:"tests,omitempty"`
	Checks               []observedVerificationCheck                   `json:"checks,omitempty"`
	Provenance           assignment.Provenance                         `json:"provenance"`
	Submission           *processworkspace.RoleOwnedSubmissionEvidence `json:"submission,omitempty"`
}

type acceptedVerificationTest struct {
	ID               string                   `json:"id"`
	Command          string                   `json:"command"`
	AssignedSelector *assignment.TestSelector `json:"assigned_selector,omitempty"`
	ResolvedRevision string                   `json:"resolved_revision,omitempty"`
	Outcome          assignment.TestOutcome   `json:"outcome"`
	Assurance        assignment.Assurance     `json:"assurance"`
}

type observedVerificationCheck struct {
	Provider        string `json:"provider"`
	Name            string `json:"name"`
	EvidenceID      string `json:"evidence_id"`
	State           string `json:"state"`
	SubjectRevision string `json:"subject_revision"`
	Source          string `json:"source"`
}

type verificationSubmitResult struct {
	OK              bool                                          `json:"ok"`
	Action          string                                        `json:"action"`
	VerificationID  string                                        `json:"verification_id"`
	ReceiptID       string                                        `json:"receipt_id"`
	ReceiptDigest   string                                        `json:"receipt_digest"`
	SubjectRevision string                                        `json:"subject_revision"`
	Tests           []acceptedVerificationTest                    `json:"tests,omitempty"`
	Checks          []observedVerificationCheck                   `json:"checks,omitempty"`
	Submission      *processworkspace.RoleOwnedSubmissionEvidence `json:"submission,omitempty"`
	CommentID       int64                                         `json:"comment_id"`
	URL             string                                        `json:"url"`
}

type finalVerifyReport struct {
	OK                    bool                          `json:"ok"`
	Traceability          model.VerifyReport            `json:"traceability"`
	Errors                []string                      `json:"errors"`
	Warnings              []string                      `json:"warnings,omitempty"`
	Diagnostics           []metadataDiagnostic          `json:"diagnostics,omitempty"`
	SpecCoverage          map[string]bool               `json:"spec_coverage"`
	RationaleCoverage     map[string]bool               `json:"rationale_coverage,omitempty"`
	Noncanonical          []model.CanonicalDiagnostic   `json:"noncanonical,omitempty"`
	ReviewFindingBlockers []reviewFinding               `json:"review_finding_blockers,omitempty"`
	FailedChecks          []reviewCheck                 `json:"failed_checks,omitempty"`
	PendingChecks         []reviewCheck                 `json:"pending_checks,omitempty"`
	PR                    int                           `json:"pr,omitempty"`
	ExternalEvidence      *externalEvidenceConsumption  `json:"external_evidence,omitempty"`
	Gate                  gates.Report                  `json:"gate"`
	ProcessEvidence       []gates.ProcessEvidenceReport `json:"process_evidence,omitempty"`
}

type finalVerifyOptions struct {
	ImplementIssue        int
	PR                    int
	PRURL                 string
	ExpectedRevision      string
	RationaleRequired     bool
	RationaleComments     []github.PullRequestReviewComment
	PRStatus              github.CombinedStatus
	PRCheckRuns           []github.CheckRun
	PRCommits             []github.PullRequestCommit
	ExternalEvidence      *externalEvidenceConsumption
	ExternalReview        *externalGateResult
	ProviderEvidence      gates.Fact
	VerifyRevision        gates.ScopedFact
	ValidationNow         time.Time
	CarrierRevisions      map[string]gates.CarrierRevisionFact
	FinalEvidenceObserved bool
}

func (a *app) runVerify(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "submit" {
		return a.runVerifySubmit(ctx, args[1:])
	}
	return a.runVerifyWithReportBuilder(ctx, args, buildFinalVerifyReport)
}

func (a *app) runVerifySubmit(ctx context.Context, args []string) int {
	fs := newFlagSet("verify submit", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	implementFlag := fs.String("implement", "", "implement issue containing the verification PROCESS")
	prFlag := fs.Int("pr", 0, "GitHub pull request number")
	processID := fs.String("process", "", "verification PROCESS id")
	verifyID := fs.String("id", "", "VERIFY id to upsert")
	resultFile := fs.String("result-file", "", "absolute path to a sealed verification receipt")
	assignmentFile := fs.String("assignment-file", "", "absolute path to the sealed verification assignment or packet")
	agent := fs.String("agent", "", "logical verification role publishing its receipt")
	agentSession := addAgentSessionFlag(fs)
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	implementIssue, err := parseIssueFlag(*implementFlag, "implement")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if strings.TrimSpace(*processID) == "" || strings.TrimSpace(*verifyID) == "" {
		a.errorf("--process and --id are required\n")
		return 2
	}
	if strings.TrimSpace(*agent) == "" {
		a.errorf("--agent is required\n")
		return 2
	}
	receipt, err := readReviewResultFile(*resultFile)
	if err != nil {
		a.errorf("read verification result: %v\n", err)
		return 2
	}
	sealedAssignment, err := readReviewAssignmentFile(*assignmentFile)
	if err != nil {
		a.errorf("read verification assignment: %v\n", err)
		return 2
	}
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for verify submit on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	process, processBody, err := findArtifactByID(ctx, client, repo, implementIssue, strings.TrimSpace(*processID))
	if err != nil {
		a.errorf("load verification PROCESS: %v\n", err)
		return 1
	}
	workspace := model.ParseProcessWorkspace(*processID, process.URL, processBody)
	class := model.ParseProcessExecutionClass(*processID, process.URL, processBody)
	if process.Comment.Type != "PROCESS" || process.Comment.ID != *processID || len(process.Comment.Errors) != 0 ||
		class.Blocking() || class.Class != model.ProcessExecutionVerification || !workspace.Explicit || workspace.Blocking() ||
		workspace.Workspace == nil || workspace.Workspace.Mode != processworkspace.ModeSnapshot {
		a.errorf("verification PROCESS must be one canonical managed snapshot assignment\n")
		return 1
	}
	submission := roleOwnedSubmissionEvidence(*agent, resolveWriterSession(*agentSession))
	if err := validateVerificationReceiptBinding(receipt, sealedAssignment, workspace.Workspace.Assignment, submission); err != nil {
		a.errorf("validate verification receipt: %v\n", err)
		return 1
	}
	if sealedAssignment.Repository != repo || sealedAssignment.Issue != int64(implementIssue) ||
		sealedAssignment.ProcessID != strings.TrimSpace(*processID) {
		a.errorf("validate verification receipt: sealed assignment repository, issue, or PROCESS identity does not match submission target\n")
		return 1
	}
	covers, err := processSectionList(processBody, "### Covers")
	if err != nil {
		a.errorf("validate verification coverage: %v\n", err)
		return 1
	}
	comments, err := client.ListIssueComments(ctx, repo, implementIssue)
	if err != nil {
		a.errorf("observe submitted VERIFY: %v\n", err)
		return 1
	}
	if err := validateExistingVerificationReceipt(comments, *verifyID, receipt); err != nil {
		a.errorf("validate submitted VERIFY replay: %v\n", err)
		return 1
	}
	specSources, err := loadSubmittedReviewSpecSources(ctx, client, repo, 0, implementIssue, process, comments)
	if err != nil {
		a.errorf("resolve submitted VERIFY SPEC authority: %v\n", err)
		return 1
	}
	var specURLs []string
	for _, id := range covers {
		if !strings.HasPrefix(id, "SPEC-") {
			continue
		}
		spec, resolveErr := findUniqueSubmittedReviewSpec(specSources, id)
		if resolveErr != nil {
			a.errorf("resolve submitted VERIFY coverage %s: %v\n", id, resolveErr)
			return 1
		}
		specURLs = append(specURLs, spec.URL)
	}
	if len(specURLs) == 0 {
		a.errorf("resolve submitted VERIFY coverage: verification PROCESS must cover at least one canonical SPEC\n")
		return 1
	}
	if 1+len(specURLs) > relationships.DefaultMutationTargetLimit {
		a.errorf("resolve submitted VERIFY coverage: %v: targets=%d limit=%d\n", relationships.ErrBound,
			1+len(specURLs), relationships.DefaultMutationTargetLimit)
		return 1
	}
	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		a.errorf("resolve verification profile: %v\n", err)
		return 1
	}
	var checks []observedVerificationCheck
	if profile.Kind == auth.ProfileKindHosted {
		if *prFlag > 0 {
			a.errorf("--pr is not a self-hosted code authority\n")
			return 2
		}
		external, selfHosted, gateErr := a.externalGate(ctx, *host, token.Value, repo, implementIssue,
			"code_change", receipt.SubjectRevision, coreevidence.GateVerify)
		if !selfHosted {
			a.errorf("self-hosted verification authority is unavailable\n")
			return 1
		}
		if gateErr != nil {
			a.errorf("observe provider verification snapshot: %v\n", gateErr)
			return 1
		}
		checks, err = observeNativeVerificationChecks(receipt.Verification.CheckSelectors, external)
	} else {
		if *prFlag <= 0 {
			a.errorf("--pr must be a positive pull request number\n")
			return 2
		}
		checks, err = observeGitHubVerificationChecks(ctx, client, repo, *prFlag, receipt)
	}
	if err != nil {
		a.errorf("observe provider verification checks: %v\n", err)
		return 1
	}
	body, err := renderSubmittedVerification(*verifyID, process.URL, covers, receipt, checks, submission, specURLs...)
	if err != nil {
		a.errorf("render submitted VERIFY: %v\n", err)
		return 1
	}
	action, comment, err := publishAcceptedVerification(ctx, client, repo, implementIssue, *verifyID, body, receipt)
	if err != nil {
		a.errorf("publish submitted VERIFY: %v\n", err)
		return 1
	}
	authority := acceptedVerificationReceiptFrom(receipt, checks, submission)
	result := verificationSubmitResult{OK: true, Action: action, VerificationID: *verifyID,
		ReceiptID: receipt.ID, ReceiptDigest: receipt.ReceiptDigest, SubjectRevision: receipt.SubjectRevision,
		Tests: authority.Tests, Checks: authority.Checks, Submission: authority.Submission, CommentID: comment.ID, URL: comment.HTMLURL}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s VERIFY %s from receipt %s at %s: %s\n", action, *verifyID, receipt.ID,
		receipt.SubjectRevision, comment.HTMLURL)
	return 0
}

func validateVerificationReceiptBinding(receipt assignment.Receipt, sealed assignment.Assignment,
	binding *processworkspace.AssignmentBinding, submission processworkspace.RoleOwnedSubmissionEvidence) error {
	if err := receipt.ValidateForAcceptance(); err != nil {
		return err
	}
	if receipt.Role != assignment.RoleVerification || receipt.Verification == nil {
		return errors.New("--result-file must contain a verification receipt")
	}
	if binding == nil || binding.SchemaVersion != assignment.AssignmentSchemaVersion ||
		binding.Role != assignment.RoleVerification || binding.BaseRevision != "" || binding.SubjectRevision == "" {
		return errors.New("PROCESS does not contain an authoritative verification assignment binding")
	}
	if receipt.AssignmentID != binding.AssignmentID || receipt.AssignmentDigest != binding.Digest ||
		receipt.AssignmentGeneration != binding.Generation {
		return errors.New("verification receipt does not match the authoritative assignment id, digest, and generation")
	}
	digest, err := assignment.AssignmentDigest(sealed)
	if err != nil {
		return fmt.Errorf("validate sealed verification assignment: %w", err)
	}
	if sealed.Role != assignment.RoleVerification || sealed.Verification == nil || sealed.ID != binding.AssignmentID ||
		digest != binding.Digest || sealed.SubjectRevision != binding.SubjectRevision || sealed.ProcessID == "" {
		return errors.New("sealed verification assignment does not exactly match the authoritative PROCESS binding")
	}
	if receipt.SubjectRevision != binding.SubjectRevision {
		return errors.New("verification receipt subject revision does not match the authoritative exact snapshot")
	}
	if err := processworkspace.ValidateRoleOwnedReceiptSubmission(receipt, submission); err != nil {
		return fmt.Errorf("verification receipt provenance: %w", err)
	}
	return assignment.ValidateVerificationReceiptCoverage(*sealed.Verification, receipt)
}

func observeGitHubVerificationChecks(ctx context.Context, client github.Backend, repo string, prNumber int,
	receipt assignment.Receipt) ([]observedVerificationCheck, error) {
	initial, err := client.GetPullRequest(ctx, repo, prNumber)
	if err != nil {
		return nil, err
	}
	initialHead := strings.TrimSpace(initial.Head.SHA)
	if initialHead == "" || initialHead != receipt.SubjectRevision {
		return nil, errors.New("verification receipt does not target the exact current PR revision")
	}
	runs, err := client.ListCheckRuns(ctx, repo, initialHead)
	if err != nil {
		return nil, err
	}
	refreshed, err := client.GetPullRequest(ctx, repo, prNumber)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(refreshed.Head.SHA) != initialHead {
		return nil, errors.New("pull request changed while observing verification checks; retry the submission")
	}
	result := make([]observedVerificationCheck, 0, len(receipt.Verification.CheckSelectors))
	for _, selector := range receipt.Verification.CheckSelectors {
		if !strings.EqualFold(strings.TrimSpace(selector.Provider), "github") {
			return nil, fmt.Errorf("check %s names unsupported GitHub provider %q", selector.Name, selector.Provider)
		}
		var selected *github.CheckRun
		for index := range runs {
			run := &runs[index]
			if run.Name != selector.Name || strings.TrimSpace(run.HeadSHA) != initialHead {
				continue
			}
			if selected == nil || run.ID > selected.ID {
				selected = run
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("provider-owned check %s is missing at revision %s", selector.Name, initialHead)
		}
		if selected.Status != "completed" {
			return nil, fmt.Errorf("provider-owned check %s is %s", selector.Name, selected.Status)
		}
		switch selected.Conclusion {
		case "success", "neutral", "skipped":
		default:
			return nil, fmt.Errorf("provider-owned check %s concluded %s", selector.Name, selected.Conclusion)
		}
		result = append(result, observedVerificationCheck{Provider: "github", Name: selector.Name,
			EvidenceID: fmt.Sprintf("%d", selected.ID), State: selected.Conclusion, SubjectRevision: initialHead,
			Source: fmt.Sprintf("github-check-run:%d", selected.ID)})
	}
	sortObservedVerificationChecks(result)
	return result, nil
}

func observeNativeVerificationChecks(selectors []assignment.CheckSelector,
	external externalGateResult) ([]observedVerificationCheck, error) {
	provider := strings.TrimSpace(external.Target.Reference.ProviderKey)
	revision := strings.TrimSpace(external.Target.SubjectRevision)
	superseded := map[string]bool{}
	for _, record := range external.Snapshot.Records {
		if id := strings.TrimSpace(record.SupersedesID); id != "" {
			superseded[id] = true
		}
	}
	result := make([]observedVerificationCheck, 0, len(selectors))
	for _, selector := range selectors {
		if strings.TrimSpace(selector.Provider) != provider {
			return nil, fmt.Errorf("check %s belongs to provider %q, not authoritative provider %q",
				selector.Name, selector.Provider, provider)
		}
		var selected *codereview.EvidenceRecord
		for index := range external.Snapshot.Records {
			record := &external.Snapshot.Records[index]
			if superseded[record.ID] || record.Kind != codereview.EvidenceCheck || record.Name != selector.Name || !record.Trusted ||
				strings.TrimSpace(record.SubjectRevision) != revision {
				continue
			}
			if selected == nil || record.ObservedAt.After(selected.ObservedAt) ||
				(record.ObservedAt.Equal(selected.ObservedAt) && record.ID > selected.ID) {
				selected = record
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("provider-owned check %s is missing at revision %s", selector.Name, revision)
		}
		switch strings.ToLower(strings.TrimSpace(selected.State)) {
		case "passed", "success", "successful":
		default:
			return nil, fmt.Errorf("provider-owned check %s is %s", selector.Name, selected.State)
		}
		result = append(result, observedVerificationCheck{Provider: provider, Name: selector.Name,
			EvidenceID: selected.ID, State: selected.State, SubjectRevision: revision,
			Source: "native-evidence:" + selected.ID})
	}
	sortObservedVerificationChecks(result)
	return result, nil
}

func sortObservedVerificationChecks(checks []observedVerificationCheck) {
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Provider != checks[j].Provider {
			return checks[i].Provider < checks[j].Provider
		}
		if checks[i].Name != checks[j].Name {
			return checks[i].Name < checks[j].Name
		}
		return checks[i].EvidenceID < checks[j].EvidenceID
	})
}

func acceptedVerificationReceiptFrom(receipt assignment.Receipt,
	checks []observedVerificationCheck, submission processworkspace.RoleOwnedSubmissionEvidence) acceptedVerificationReceipt {
	evidence := submission
	result := acceptedVerificationReceipt{ReceiptID: receipt.ID, ReceiptDigest: receipt.ReceiptDigest,
		AssignmentID: receipt.AssignmentID, AssignmentDigest: receipt.AssignmentDigest,
		AssignmentGeneration: receipt.AssignmentGeneration, SubjectRevision: receipt.SubjectRevision,
		Checks: append([]observedVerificationCheck(nil), checks...), Provenance: receipt.Provenance, Submission: &evidence}
	for _, test := range receipt.Tests {
		projected := acceptedVerificationTest{ID: test.ID, Command: test.Command,
			ResolvedRevision: test.ResolvedRevision, Outcome: test.Outcome, Assurance: test.Assurance}
		if test.AssignedSelector != nil {
			selector := cloneFinalTestSelector(*test.AssignedSelector)
			projected.AssignedSelector = &selector
		}
		result.Tests = append(result.Tests, projected)
	}
	sort.Slice(result.Tests, func(i, j int) bool { return result.Tests[i].ID < result.Tests[j].ID })
	sortObservedVerificationChecks(result.Checks)
	return result
}

func cloneFinalTestSelector(value assignment.TestSelector) assignment.TestSelector {
	clone := value
	if value.RevisionBinding != nil {
		binding := *value.RevisionBinding
		clone.RevisionBinding = &binding
	}
	return clone
}

func stampAcceptedVerificationReceipt(body string, receipt acceptedVerificationReceipt) (string, bool, error) {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return "", false, err
	}
	block := acceptedVerificationReceiptStart + "\n" + string(raw) + "\n" + acceptedVerificationReceiptEnd
	startCount, endCount := strings.Count(body, acceptedVerificationReceiptStart), strings.Count(body, acceptedVerificationReceiptEnd)
	if startCount != endCount || startCount > 1 ||
		strings.Count(body, "issue-spec:accepted-verification-receipt") != startCount+endCount {
		return "", false, errors.New("existing accepted verification receipt block is malformed")
	}
	if startCount == 0 {
		return strings.TrimRight(body, "\n") + "\n\n" + block + "\n", true, nil
	}
	start, end := strings.Index(body, acceptedVerificationReceiptStart), strings.Index(body, acceptedVerificationReceiptEnd)
	if end < start+len(acceptedVerificationReceiptStart) {
		return "", false, errors.New("existing accepted verification receipt block is malformed")
	}
	end += len(acceptedVerificationReceiptEnd)
	if body[start:end] != block {
		return "", false, errors.New("accepted verification receipt authority is immutable")
	}
	return body, false, nil
}

func parseAcceptedVerificationReceipt(body string) (acceptedVerificationReceipt, bool, error) {
	if !strings.Contains(body, "issue-spec:accepted-verification-receipt") {
		return acceptedVerificationReceipt{}, false, nil
	}
	if strings.Count(body, acceptedVerificationReceiptStart) != 1 ||
		strings.Count(body, acceptedVerificationReceiptEnd) != 1 ||
		strings.Count(body, "issue-spec:accepted-verification-receipt") != 2 {
		return acceptedVerificationReceipt{}, true, errors.New("accepted verification receipt must contain exactly one version-1 marker pair")
	}
	start, end := strings.Index(body, acceptedVerificationReceiptStart), strings.Index(body, acceptedVerificationReceiptEnd)
	if end <= start {
		return acceptedVerificationReceipt{}, true, errors.New("accepted verification receipt marker order is invalid")
	}
	rawBlock := body[start+len(acceptedVerificationReceiptStart) : end]
	if len(rawBlock) < 3 || rawBlock[0] != '\n' || rawBlock[len(rawBlock)-1] != '\n' {
		return acceptedVerificationReceipt{}, true, errors.New("accepted verification receipt payload framing is invalid")
	}
	raw := []byte(rawBlock[1 : len(rawBlock)-1])
	var result acceptedVerificationReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return acceptedVerificationReceipt{}, true, fmt.Errorf("decode accepted verification receipt: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return acceptedVerificationReceipt{}, true, errors.New("accepted verification receipt has trailing JSON")
	}
	canonical, _ := json.Marshal(result)
	if !bytes.Equal(raw, canonical) {
		return acceptedVerificationReceipt{}, true, errors.New("accepted verification receipt payload is not canonical JSON")
	}
	return result, true, nil
}

func validateExistingVerificationReceipt(comments []github.Comment, verifyID string, receipt assignment.Receipt) error {
	_, _, err := observeAcceptedVerificationReceipt(comments, verifyID, receipt)
	return err
}

func observeAcceptedVerificationReceipt(comments []github.Comment, verifyID string,
	receipt assignment.Receipt) (github.Comment, bool, error) {
	var exact github.Comment
	exactCount := 0
	for _, comment := range comments {
		parsed := model.ParseTypedComment(comment.Body)
		if parsed.Type != "VERIFY" {
			continue
		}
		existing, found, err := parseAcceptedVerificationReceipt(comment.Body)
		if err != nil {
			return github.Comment{}, false, fmt.Errorf("VERIFY %s: %w", parsed.ID, err)
		}
		if !found {
			if parsed.ID == verifyID {
				return github.Comment{}, false, fmt.Errorf("VERIFY %s already exists without accepted receipt authority", verifyID)
			}
			continue
		}
		sameGeneration := existing.AssignmentID == receipt.AssignmentID &&
			existing.AssignmentDigest == receipt.AssignmentDigest && existing.AssignmentGeneration == receipt.AssignmentGeneration
		if sameGeneration && existing.ReceiptDigest != receipt.ReceiptDigest {
			return github.Comment{}, false, fmt.Errorf("assignment generation already accepted different receipt %s", existing.ReceiptID)
		}
		if existing.ReceiptID == receipt.ID && existing.ReceiptDigest != receipt.ReceiptDigest {
			return github.Comment{}, false, fmt.Errorf("receipt id %s already exists with different digest", receipt.ID)
		}
		if existing.ReceiptDigest == receipt.ReceiptDigest && parsed.ID != verifyID {
			return github.Comment{}, false, fmt.Errorf("receipt %s is already projected by VERIFY %s", receipt.ID, parsed.ID)
		}
		if parsed.ID == verifyID && existing.ReceiptDigest != receipt.ReceiptDigest {
			return github.Comment{}, false, fmt.Errorf("VERIFY %s already carries different receipt authority", verifyID)
		}
		if parsed.ID == verifyID && existing.ReceiptDigest == receipt.ReceiptDigest {
			exact, exactCount = comment, exactCount+1
		}
	}
	if exactCount > 1 {
		return github.Comment{}, false, fmt.Errorf("VERIFY %s has duplicate accepted receipt authority", verifyID)
	}
	return exact, exactCount == 1, nil
}

func publishAcceptedVerification(ctx context.Context, client github.Operations, repo string, issue int,
	verifyID, body string, receipt assignment.Receipt) (string, github.Comment, error) {
	comments, err := client.ListIssueComments(ctx, repo, issue)
	if err != nil {
		return "", github.Comment{}, err
	}
	existing, found, err := observeAcceptedVerificationReceipt(comments, verifyID, receipt)
	if err != nil {
		return "", github.Comment{}, err
	}
	if found {
		if existing.Body != body {
			return "", github.Comment{}, fmt.Errorf("VERIFY %s accepted authority exists with a different immutable body", verifyID)
		}
		return "unchanged", existing, nil
	}
	created, err := client.CreateComment(ctx, repo, issue, body)
	if err != nil {
		return "", github.Comment{}, err
	}
	comments, err = client.ListIssueComments(ctx, repo, issue)
	if err != nil {
		return "", github.Comment{}, fmt.Errorf("re-observe accepted VERIFY after create: %w", err)
	}
	observed, found, err := observeAcceptedVerificationReceipt(comments, verifyID, receipt)
	if err != nil {
		return "", github.Comment{}, fmt.Errorf("accepted VERIFY publication conflicted: %w", err)
	}
	if !found || observed.ID != created.ID {
		return "", github.Comment{}, errors.New("accepted VERIFY publication was not observed as one unique append-only authority")
	}
	return "created", created, nil
}

func renderSubmittedVerification(verifyID, processURL string, covers []string, receipt assignment.Receipt,
	checks []observedVerificationCheck, submission processworkspace.RoleOwnedSubmissionEvidence, specURLs ...string) (string, error) {
	tests := make([]templates.VerifyTestEvidence, 0, len(receipt.Tests))
	for _, test := range receipt.Tests {
		tests = append(tests, templates.VerifyTestEvidence{ID: test.ID, Command: test.Command,
			Outcome: string(test.Outcome), Assurance: string(test.Assurance)})
	}
	providerChecks := make([]templates.VerifyCheckEvidence, 0, len(checks))
	for _, check := range checks {
		providerChecks = append(providerChecks, templates.VerifyCheckEvidence{Provider: check.Provider, Name: check.Name,
			State: check.State, SubjectRevision: check.SubjectRevision, Source: check.Source})
	}
	summary := strings.TrimSpace(receipt.Verification.Summary)
	if summary == "" {
		summary = "Role-owned verification completed for the exact assigned revision."
	}
	body, err := templates.VerifyComment(templates.VerifyCommentOptions{Common: templates.CommonOptions{
		ID: verifyID, Agent: submission.Agent, SubjectRevision: receipt.SubjectRevision,
		Status: "done", Scope: "role-owned verification submission"}, Input: templates.VerifyInput{
		Title: "role-owned receipt", Summary: summary, SubjectRevision: receipt.SubjectRevision,
		Tests: tests, Checks: providerChecks, SpecRefs: covers}})
	if err != nil {
		return "", err
	}
	body, _, err = model.AddRelatedCommentLink(body, processURL)
	if err != nil {
		return "", err
	}
	for _, specURL := range specURLs {
		body, _, err = model.AddRelatedCommentLink(body, specURL)
		if err != nil {
			return "", err
		}
	}
	body, _, err = stampAcceptedVerificationReceipt(body, acceptedVerificationReceiptFrom(receipt, checks, submission))
	return body, err
}

func (a *app) runVerifyWithReportBuilder(ctx context.Context, args []string,
	buildReport func([]model.Artifact, string, finalVerifyOptions) (finalVerifyReport, error)) int {
	fs := newFlagSet("verify", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	proposalFlag := fs.String("proposal", "", "proposal issue number or URL")
	designFlag := fs.String("design", "", "design issue number or URL")
	implementFlag := fs.String("implement", "", "implement issue number or URL")
	prFlag := fs.Int("pr", 0, "pull request number for rationale-comment verification")
	revision := fs.String("revision", "", "expected external code head revision for self-hosted evidence")
	jsonOut := fs.Bool("json", false, "write JSON output")
	summaryOut := fs.Bool("summary", false, "write compact versioned JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if *summaryOut && !*jsonOut {
		a.errorf("--summary requires --json\n")
		return 2
	}
	prProvided := false
	fs.Visit(func(current *flag.Flag) {
		if current.Name == "pr" {
			prProvided = true
		}
	})
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	proposalIssue, err := parseIssueFlag(*proposalFlag, "proposal")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	designIssue, err := parseIssueFlag(*designFlag, "design")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	implementIssue, err := parseIssueFlag(*implementFlag, "implement")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for verify on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	proposalIssueData, err := client.GetIssue(ctx, repo, proposalIssue)
	if err != nil {
		a.errorf("read proposal issue #%d: %v\n", proposalIssue, err)
		return 1
	}
	artifacts, err := collectArtifacts(ctx, client, repo, proposalIssue, designIssue, implementIssue)
	if err != nil {
		a.errorf("collect artifacts: %v\n", err)
		return 1
	}
	var rationaleComments []github.PullRequestReviewComment
	var prStatus github.CombinedStatus
	var prCheckRuns []github.CheckRun
	var prCommits []github.PullRequestCommit
	var prURL string
	var expectedRevision string
	externalGate, selfHosted, externalGateErr := a.externalGate(ctx, *host, token.Value, repo, implementIssue,
		"code_change", *revision, coreevidence.GateVerify)
	if selfHosted && prProvided {
		a.errorf("--pr is not a self-hosted code authority; omit it and use the active code_change reference\n")
		return 2
	}
	if externalGateErr != nil && !selfHosted {
		a.errorf("verify external evidence: %v\n", externalGateErr)
		return 1
	}
	if !selfHosted && *prFlag <= 0 && hasActiveChangeBearingProcess(artifactsForImplementGate(artifacts, implementIssue)) {
		a.errorf("--pr is required for GitHub verify when an active change-bearing PROCESS exists\n")
		return 2
	}
	if !selfHosted && *prFlag > 0 {
		facts, err := collectPullRequestGateFacts(ctx, client, repo, *prFlag)
		if err != nil {
			a.errorf("read stable PR #%d gate facts: %v\n", *prFlag, err)
			return 1
		}
		pr := facts.PullRequest
		prURL = pr.HTMLURL
		expectedRevision = pr.Head.SHA
		rationaleComments = facts.ReviewComments
		prStatus = facts.Status
		prCheckRuns = facts.CheckRuns
		prCommits = facts.Commits
	}
	var processExternalEvidence *externalEvidenceConsumption
	var processExternalReview *externalGateResult
	var providerEvidence gates.Fact
	var verifyRevision gates.ScopedFact
	var finalVerify *model.Artifact
	if selfHosted && externalGateErr != nil {
		// Preserve the legacy stderr detail while also carrying this provider
		// failure into the authoritative evaluator result used by JSON/summary
		// consumers and the final exit decision.
		a.errorf("verify external evidence: %v\n", externalGateErr)
		providerEvidence = gates.Fact{Required: true, Known: true, Passed: false,
			Current: externalGateErr.Error(), Expected: "trusted exact-revision provider evidence"}
	}
	if selfHosted && externalGateErr == nil {
		processExternalEvidence = &externalGate.Consumption
		processExternalReview = &externalGate
		expectedRevision = externalGate.Target.SubjectRevision
		providerEvidence = gates.Fact{Required: true, Known: true, Passed: true,
			Current: expectedRevision, Expected: "trusted exact-revision provider evidence"}
		verifyRevision, finalVerify = collectVerifyRevisionFact(artifacts, expectedRevision, time.Now().UTC())
	}
	report, err := buildReport(artifacts, proposalIssueData.HTMLURL, finalVerifyOptions{
		ImplementIssue:        implementIssue,
		PR:                    *prFlag,
		PRURL:                 prURL,
		ExpectedRevision:      expectedRevision,
		RationaleRequired:     *prFlag > 0,
		RationaleComments:     rationaleComments,
		PRStatus:              prStatus,
		PRCheckRuns:           prCheckRuns,
		PRCommits:             prCommits,
		ExternalEvidence:      processExternalEvidence,
		ExternalReview:        processExternalReview,
		ProviderEvidence:      providerEvidence,
		VerifyRevision:        verifyRevision,
		ValidationNow:         time.Now().UTC(),
		FinalEvidenceObserved: strings.TrimSpace(expectedRevision) != "" && (*prFlag > 0 || processExternalReview != nil),
	})
	if err != nil {
		a.errorf("verify: %v\n", err)
		return 1
	}
	if selfHosted && externalGateErr == nil && finalVerify != nil {
		report.ExternalEvidence = &externalGate.Consumption
	}
	report.Diagnostics = append(report.Diagnostics, authoringCompletenessDiagnostics("proposal", proposalIssueData.HTMLURL, proposalIssueData.Body)...)
	if designIssue > 0 {
		if designIssueData, derr := client.GetIssue(ctx, repo, designIssue); derr == nil {
			report.Diagnostics = append(report.Diagnostics, authoringCompletenessDiagnostics("design", designIssueData.HTMLURL, designIssueData.Body)...)
		}
	}
	if selfHosted && externalGateErr == nil && report.OK && finalVerify != nil && len(externalGate.Consumption.Bindings) > 0 {
		updated, changed, stampErr := stampConsumedEvidence(finalVerify.Comment.Body, externalGate.Consumption)
		if stampErr != nil {
			a.errorf("record consumed external evidence: %v\n", stampErr)
			return 1
		}
		if changed {
			if _, updateErr := client.UpdateComment(ctx, repo, finalVerify.CommentID, updated); updateErr != nil {
				a.errorf("record consumed external evidence on %s: %v\n", finalVerify.Comment.ID, updateErr)
				return 1
			}
		}
	}
	if *jsonOut {
		var output any = report
		if *summaryOut {
			var subject *gates.CompactSubject
			if selfHosted {
				subject = compactExternalSubject(externalGate)
			} else {
				subject = compactPullRequestSubject(*prFlag, prURL, expectedRevision)
			}
			output = gates.ProjectCompactSummary(report.Gate, artifactStatusCounts(artifacts), subject,
				gates.Remediation{CommandFamily: "verify", Arguments: compactDetailArguments(args)})
		}
		if code := a.outputJSON(output); code != 0 {
			return code
		}
		if !report.OK {
			return 1
		}
		return 0
	}
	printFinalVerify(a.out, report)
	if !report.OK {
		return 1
	}
	return 0
}

func hasActiveChangeBearingProcess(artifacts []model.Artifact) bool {
	for _, artifact := range activeProcessArtifacts(artifacts) {
		if model.ParseProcessExecutionClass(artifact.Comment.ID, artifact.URL, artifact.Comment.Body).Class == model.ProcessExecutionChangeBearing {
			return true
		}
	}
	return false
}

const (
	consumedEvidenceStart = "<!-- issue-spec:consumed-evidence version=1 -->"
	consumedEvidenceEnd   = "<!-- /issue-spec:consumed-evidence -->"
)

func exactRevisionBoundVerify(artifacts []model.Artifact, revision string) (*model.Artifact, error) {
	var candidates []*model.Artifact
	for index := range artifacts {
		if artifacts[index].Comment.Type == "VERIFY" && artifacts[index].Comment.Status == "done" {
			candidates = append(candidates, &artifacts[index])
		}
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("self-hosted verify requires exactly one active done VERIFY (found %d)", len(candidates))
	}
	raw := strings.TrimSpace(sectionContent(candidates[0].Comment.Body, "### Revision"))
	raw = strings.Trim(raw, "`")
	if fields := strings.Fields(raw); len(fields) != 1 || fields[0] != revision {
		return candidates[0], fmt.Errorf("%s must contain `### Revision` with exact external head revision %s", candidates[0].Comment.ID, revision)
	}
	return candidates[0], nil
}

func collectVerifyRevisionFact(artifacts []model.Artifact, revision string, observedAt time.Time) (gates.ScopedFact, *model.Artifact) {
	revision = strings.TrimSpace(revision)
	fact := gates.ScopedFact{Fact: gates.Fact{Required: true, Known: revision != "", Expected: revision}}
	if !observedAt.IsZero() {
		observedAt = observedAt.UTC()
		fact.Fact.ObservedAt = &observedAt
	}
	if revision == "" {
		return fact, nil
	}
	candidate, err := exactRevisionBoundVerify(artifacts, revision)
	if candidate != nil {
		fact.Artifact = gates.ArtifactRef{Type: candidate.Comment.Type, ID: candidate.Comment.ID, URL: candidate.URL}
	}
	fact.Fact.Passed = err == nil
	if err != nil {
		fact.Fact.Current = err.Error()
		return fact, nil
	}
	fact.Fact.Current = revision
	return fact, candidate
}

func stampConsumedEvidence(body string, consumption externalEvidenceConsumption) (string, bool, error) {
	consumption.EvidenceIDs = append([]string(nil), consumption.EvidenceIDs...)
	sort.Strings(consumption.EvidenceIDs)
	consumption.Bindings = normalizeExternalEvidenceBindings(append([]externalEvidenceBinding(nil), consumption.Bindings...))
	if consumption.ProviderKey == "" || consumption.ExternalRepository == "" || consumption.ChangeID == "" ||
		consumption.SubjectRevision == "" || len(consumption.EvidenceIDs) == 0 || len(consumption.Bindings) == 0 {
		return "", false, errors.New("consumed evidence identity is incomplete")
	}
	raw, err := json.Marshal(consumption)
	if err != nil {
		return "", false, err
	}
	block := consumedEvidenceStart + "\n### Consumed External Evidence\n\n```json\n" + string(raw) + "\n```\n" + consumedEvidenceEnd
	startCount, endCount := strings.Count(body, consumedEvidenceStart), strings.Count(body, consumedEvidenceEnd)
	if startCount != endCount || startCount > 1 {
		return "", false, errors.New("existing consumed evidence block is malformed")
	}
	start, end := strings.Index(body, consumedEvidenceStart), strings.Index(body, consumedEvidenceEnd)
	if startCount == 1 && end < start+len(consumedEvidenceStart) {
		return "", false, errors.New("existing consumed evidence block is malformed")
	}
	updated := body
	if start >= 0 {
		end += len(consumedEvidenceEnd)
		updated = body[:start] + block + body[end:]
	} else {
		updated = strings.TrimRight(body, "\n") + "\n\n" + block + "\n"
	}
	return updated, updated != body, nil
}

func buildFinalVerifyReport(artifacts []model.Artifact, _ string, opts finalVerifyOptions) (finalVerifyReport, error) {
	useMinimalFinal := opts.FinalEvidenceObserved
	gateArtifacts := artifactsForImplementGate(artifacts, opts.ImplementIssue)
	relationshipIndex, relationshipErr := relationships.BuildIndex(gateArtifacts)
	traceability := model.VerifyReport{OK: true}
	if !useMinimalFinal {
		traceability = model.VerifyTraceability(artifacts)
		if relationshipErr == nil {
			traceability = mergeVerifyReports(traceability,
				model.VerifyTraceabilityWithRelationships(gateArtifacts, commandTraceabilityEdges(relationshipIndex), nil))
		}
	}
	report := finalVerifyReport{
		Traceability:      traceability,
		SpecCoverage:      map[string]bool{},
		RationaleCoverage: map[string]bool{},
		PR:                opts.PR,
	}
	var activeSpecs []model.Artifact
	activeProcesses := activeProcessArtifacts(gateArtifacts)
	activeProcessSet := activeProcessIDs(gateArtifacts)
	var doneVerifyBodies []string
	var canonical []model.CanonicalDiagnostic
	for _, artifact := range artifacts {
		tc := artifact.Comment
		switch tc.Type {
		case "SPEC":
			if tc.Status != "superseded" {
				activeSpecs = append(activeSpecs, artifact)
				report.SpecCoverage[tc.ID] = false
			}
		case "PROCESS":
		case "VERIFY":
			if tc.Status == "done" {
				doneVerifyBodies = append(doneVerifyBodies, tc.Body)
			}
		}
		if (tc.Type == "PROCESS" && (opts.ImplementIssue <= 0 || artifact.Issue == opts.ImplementIssue) && !activeProcessSet[tc.ID]) ||
			(tc.Type != "PROCESS" && artifact.Comment.Status == "superseded") {
			continue
		}
		if !useMinimalFinal {
			diags := model.ValidateArtifact(artifact)
			canonical = append(canonical, diags...)
			report.Noncanonical = append(report.Noncanonical, diags...)
		}
	}
	if !useMinimalFinal {
		verifyText := strings.Join(doneVerifyBodies, "\n")
		for _, spec := range activeSpecs {
			if strings.Contains(verifyText, spec.Comment.ID) {
				report.SpecCoverage[spec.Comment.ID] = true
			}
		}
	}

	var reviewReport reviewSyncReport
	remote := gates.RemoteFacts{ProviderEvidence: opts.ProviderEvidence, VerifyRevision: opts.VerifyRevision}
	if opts.RationaleRequired {
		pr := github.PullRequest{Number: opts.PR, HTMLURL: opts.PRURL}
		pr.Head.SHA = strings.TrimSpace(opts.ExpectedRevision)
		reviewReport = buildReviewSyncReport(pr, opts.RationaleComments, nil, opts.PRStatus, opts.PRCheckRuns)
		remote.PRChecks = gates.Fact{Required: true, Known: true,
			Passed:   len(reviewReport.FailedChecks) == 0 && len(reviewReport.PendingChecks) == 0,
			Current:  fmt.Sprintf("failed=%d pending=%d", len(reviewReport.FailedChecks), len(reviewReport.PendingChecks)),
			Expected: "failed=0 pending=0"}
		remote.ReviewFindings = gates.Fact{Required: true, Known: true,
			Passed:  len(reviewReport.BlockingFindings) == 0,
			Current: fmt.Sprintf("blocking=%d", len(reviewReport.BlockingFindings)), Expected: "blocking=0"}
	}
	if opts.ExternalReview != nil {
		remote.ReviewFindings = gates.Fact{Required: true, Known: true, Passed: true,
			Current: "blocking=0", Expected: "blocking=0"}
	}

	target := gates.TargetFinal
	var processEvidence []gates.ProcessEvidenceInput
	if opts.RationaleRequired || opts.ExternalEvidence != nil || hasExplicitProcessWorkspace(gateArtifacts) || hasActiveChangeBearingProcess(gateArtifacts) {
		if opts.ExternalReview != nil {
			validationNow := opts.ValidationNow
			if validationNow.IsZero() {
				validationNow = time.Now().UTC()
			}
			processEvidence = buildProcessEvidenceInputsWithExternalReview(gateArtifacts, opts.PRURL, opts.RationaleComments,
				reviewReport, opts.ExternalEvidence, opts.ExternalReview, validationNow)
		} else {
			processEvidence = buildProcessEvidenceInputs(gateArtifacts, opts.PRURL, opts.RationaleComments, reviewReport, opts.ExternalEvidence)
		}
		processEvidence = consumeAcceptedVerificationEvidence(processEvidence, gateArtifacts, opts.ExpectedRevision)
	}
	subjectKind, subjectURL, subjectSource, subjectTrusted := "pull_request", opts.PRURL,
		fmt.Sprintf("github-pull-request-head:%d", opts.PR), opts.PR > 0 && strings.TrimSpace(opts.ExpectedRevision) != ""
	if opts.ExternalReview != nil {
		subjectKind, subjectURL, subjectSource = "code_change", opts.ExternalReview.Target.CanonicalURL, "native-authoritative-ledger:code-subject"
		subjectTrusted = opts.ProviderEvidence.Known && opts.ProviderEvidence.Passed
	}
	var finalEvidence gates.FinalEvidenceSnapshot
	if useMinimalFinal {
		finalEvidence = buildMinimalFinalEvidence(gateArtifacts, processEvidence, gates.FinalSubject{
			Required: true, Known: strings.TrimSpace(opts.ExpectedRevision) != "", Trusted: subjectTrusted,
			Kind: subjectKind, URL: subjectURL, Revision: strings.TrimSpace(opts.ExpectedRevision), Source: subjectSource,
		})
	}
	gateReport, err := gates.Evaluate(gates.Snapshot{
		Target: target, Mode: gates.ModeAuthoritative, Artifacts: gateArtifacts,
		Canonical:                gates.CanonicalFacts{Observed: true, Diagnostics: canonical},
		Traceability:             gates.TraceabilityFacts{Observed: true, Report: traceability},
		Relationships:            gates.RelationshipFacts{Observed: relationshipErr == nil, Index: relationshipIndex},
		Remote:                   remote,
		ProcessEvidence:          processEvidence,
		FinalEvidence:            finalEvidence,
		LegacyFinalCompatibility: !useMinimalFinal,
	})
	if err != nil {
		return report, err
	}
	var workspaceGateDiagnostics []gates.Diagnostic
	if !useMinimalFinal {
		workspaceReport, workspaceErr := gates.EvaluateWorkspaceEvidence(gates.WorkspaceEvaluationInput{
			Target: target, Mode: gates.ModeAuthoritative, Artifacts: currentFinalizationArtifacts(gateArtifacts),
			ExpectedRevision:    gates.Fact{Required: true, Known: strings.TrimSpace(opts.ExpectedRevision) != "", Passed: true, Expected: strings.TrimSpace(opts.ExpectedRevision)},
			IntegrationAncestry: pullRequestIntegrationAncestry(gateArtifacts, opts.PRCommits, opts.ExpectedRevision),
			ProcessEvidence:     gateReport.Processes,
			CarrierRevisions:    mergeCarrierRevisionFacts(gates.ProcessCarrierRevisionFacts(gateReport.Processes), opts.CarrierRevisions),
		})
		if workspaceErr != nil {
			return report, workspaceErr
		}
		for _, diagnostic := range workspaceReport.Diagnostics {
			if !diagnostic.Blocking && diagnostic.Severity == gates.SeverityWarning {
				report.Warnings = append(report.Warnings, diagnostic.Message)
				continue
			}
			workspaceGateDiagnostics = append(workspaceGateDiagnostics, diagnostic)
		}
	}
	gateReport.Diagnostics = append(gateReport.Diagnostics, workspaceGateDiagnostics...)
	sort.SliceStable(gateReport.Diagnostics, func(i, j int) bool {
		if gateReport.Diagnostics[i].Code != gateReport.Diagnostics[j].Code {
			return gateReport.Diagnostics[i].Code < gateReport.Diagnostics[j].Code
		}
		return gateReport.Diagnostics[i].Artifact.ID < gateReport.Diagnostics[j].Artifact.ID
	})
	for _, diagnostic := range workspaceGateDiagnostics {
		if diagnostic.Blocking {
			gateReport.Ready = false
		}
	}
	report.Gate = gateReport
	report.ProcessEvidence = gateReport.Processes
	for _, diagnostic := range gateReport.Diagnostics {
		if diagnostic.Code == gates.CodeProcessExecutionClassLegacy {
			report.Warnings = append(report.Warnings, diagnostic.Message)
		}
		if message, ok := legacyVerifyGateError(diagnostic); ok {
			report.Errors = append(report.Errors, message)
		}
	}

	if opts.RationaleRequired {
		for _, process := range activeProcesses {
			report.RationaleCoverage[process.Comment.ID] = false
		}
		for _, process := range report.ProcessEvidence {
			for _, satisfied := range process.Satisfied {
				if satisfied == "matching inline rationale" {
					report.RationaleCoverage[process.ProcessID] = true
				}
			}
		}
		report.Diagnostics = append(report.Diagnostics, reviewReport.Diagnostics...)
		report.ReviewFindingBlockers = reviewReport.BlockingFindings
		for _, finding := range report.ReviewFindingBlockers {
			report.Errors = append(report.Errors, fmt.Sprintf("open %s review finding %s on %s:%d", finding.Severity, finding.ID, finding.Path, finding.Line))
		}
		if !useMinimalFinal {
			report.FailedChecks = reviewReport.FailedChecks
			report.PendingChecks = reviewReport.PendingChecks
			for _, check := range report.FailedChecks {
				report.Errors = append(report.Errors, fmt.Sprintf("PR check %s failed state=%s conclusion=%s", check.Name, check.State, check.Conclusion))
			}
			for _, check := range report.PendingChecks {
				report.Errors = append(report.Errors, fmt.Sprintf("PR check %s is pending state=%s conclusion=%s", check.Name, check.State, check.Conclusion))
			}
		}
	}
	if !opts.RationaleRequired {
		report.RationaleCoverage = nil
	}
	sort.Strings(report.Errors)
	sort.Strings(report.Warnings)
	// Errors is now only a compatibility projection for the full report. Every
	// blocker represented there is collected into gateReport first, so the shared
	// authoritative decision is the sole source of OK and the exit status.
	report.OK = gateReport.Ready
	return report, nil
}

type finalEvidenceMetadata struct {
	name        string
	independent bool
}

func buildMinimalFinalEvidence(artifacts []model.Artifact, inputs []gates.ProcessEvidenceInput,
	subject gates.FinalSubject) gates.FinalEvidenceSnapshot {
	result := gates.FinalEvidenceSnapshot{Observed: true, Subject: subject,
		Index: gates.Fact{Required: true, Known: true, Expected: "validated bounded exact-current index"}}
	revision := strings.TrimSpace(subject.Revision)
	byURL := map[string]model.Artifact{}
	for _, artifact := range artifacts {
		if artifact.URL != "" {
			byURL[model.NormalizeURL(artifact.URL)] = artifact
		}
	}
	var records []CanonicalEvidenceRecord
	metadata := map[string]finalEvidenceMetadata{}
	activeAssignments := map[string]gates.ActiveAssignmentEvidence{}
	processedRoleEvidence := map[string]bool{}
	for _, input := range inputs {
		if input.ActiveAssignment != nil {
			activeAssignments[input.ActiveAssignment.ProcessID] = *input.ActiveAssignment
		}
	}
	add := func(record CanonicalEvidenceRecord, name string, independent bool) {
		records = append(records, record)
		metadata[finalEvidenceMetadataKey(record)] = finalEvidenceMetadata{name: name, independent: independent}
	}
	addTargets := func(record CanonicalEvidenceRecord, targets []string, name string, independent bool) {
		for _, targetProcessID := range targets {
			projected := record
			projected.ProcessID = targetProcessID
			add(projected, name, independent)
		}
	}
	for _, input := range inputs {
		processID := input.Process.Comment.ID
		addCovered := func(record CanonicalEvidenceRecord, name string, independent bool) {
			addTargets(record, finalEvidenceProcessTargets(artifacts, input, record.SpecID), name, independent)
		}
		for _, evidence := range input.Reviews {
			if evidence.ProcessID != processID || evidence.SpecID == "" || !(evidence.Done || evidence.FindingResolved) {
				continue
			}
			independent := evidence.ReviewerAgent != "" &&
				!input.AuthorAgentsBySpec[evidence.SpecID][strings.ToLower(strings.TrimSpace(evidence.ReviewerAgent))]
			artifact := byURL[model.NormalizeURL(evidence.URL)]
			if authority, ok := exactAcceptedReviewCarrier(artifact, revision); ok {
				assignmentProcessID, err := acceptedRoleAssignmentProcess(inputs, processID, evidence.URL, evidence.SpecID,
					assignment.RoleReview, authority.AssignmentID, authority.AssignmentDigest,
					authority.AssignmentGeneration, authority.SubjectRevision, authority.AssignmentProcessID)
				if err != nil {
					result.Index.Current = err.Error()
					return result
				}
				carrierKey := strings.Join([]string{string(assignment.RoleReview), model.NormalizeURL(evidence.URL),
					evidence.SpecID, authority.ReceiptID, authority.ReceiptDigest}, "\x00")
				if processedRoleEvidence[carrierKey] {
					continue
				}
				processedRoleEvidence[carrierKey] = true
				sourceInput, found := processEvidenceInputByID(inputs, assignmentProcessID)
				if !found {
					result.Index.Current = fmt.Sprintf("accepted review receipt source PROCESS %s is unavailable", assignmentProcessID)
					return result
				}
				writer := strings.ToLower(strings.TrimSpace(authority.Provenance.Writer))
				independent = writer != "" && !sourceInput.AuthorAgentsBySpec[evidence.SpecID][writer]
				base := CanonicalEvidenceRecord{ProcessID: processID, SpecID: evidence.SpecID,
					Authority: CanonicalEvidenceRoleOwned, EvidenceID: authority.ReceiptID, ReceiptID: authority.ReceiptID,
					ReceiptDigest: authority.ReceiptDigest, AssignmentProcessID: assignmentProcessID, AssignmentID: authority.AssignmentID,
					AssignmentDigest: authority.AssignmentDigest, AssignmentGeneration: authority.AssignmentGeneration,
					AssignmentRole:  assignment.RoleReview,
					SubjectRevision: revision, URL: artifact.URL,
					Source: "accepted-review-receipt:self-reported", Trusted: true}
				if authority.CarrierVersion == 1 {
					base.Source = "accepted-review-receipt:v1-completion"
				}
				targets := roleOwnedEvidenceTargets(inputs, assignmentProcessID, evidence.URL, evidence.SpecID,
					assignment.RoleReview)
				review := base
				review.Kind, review.EvidenceID = CanonicalEvidenceReview, authority.ReceiptID
				addTargets(review, targets, "", independent)
				if !authority.TestsAvailable {
					continue
				}
				for _, test := range authority.Tests {
					record := base
					record.Kind, record.EvidenceID = CanonicalEvidenceTest, authority.ReceiptID+":test:"+test.ID
					record.TestID = test.ID
					record.ResolvedRevision, record.ExecutedCommand = test.ResolvedRevision, test.Command
					if test.AssignedSelector != nil {
						selector := cloneFinalTestSelector(*test.AssignedSelector)
						record.AssignedSelector = &selector
					}
					addTargets(record, targets, test.ID, independent)
				}
				continue
			}
			if !evidence.Trusted || evidence.SubjectRevision != revision {
				continue
			}
			source := canonicalFinalProviderSource(evidence.Source, artifact.Comment.ID)
			if source == "" {
				continue
			}
			addCovered(CanonicalEvidenceRecord{ProcessID: processID, SpecID: evidence.SpecID, Kind: CanonicalEvidenceReview,
				Authority: CanonicalEvidenceProviderOwned, EvidenceID: source + ":" + model.NormalizeURL(evidence.URL),
				SubjectRevision: revision, URL: evidence.URL, Source: source, Trusted: true}, "", independent)
		}
		for _, evidence := range input.Verifications {
			if evidence.ProcessID != processID || evidence.SpecID == "" || !evidence.Done {
				continue
			}
			artifact := byURL[model.NormalizeURL(evidence.URL)]
			authority, found, err := parseAcceptedVerificationReceipt(artifact.Comment.Body)
			if err != nil || !found {
				continue
			}
			source, _, _, valid := exactAcceptedVerificationCarrier(artifact, revision)
			if !valid {
				continue
			}
			assignmentProcessID, err := acceptedRoleAssignmentProcess(inputs, processID, evidence.URL, evidence.SpecID,
				assignment.RoleVerification, authority.AssignmentID, authority.AssignmentDigest,
				authority.AssignmentGeneration, authority.SubjectRevision, "")
			if err != nil {
				result.Index.Current = err.Error()
				return result
			}
			carrierKey := strings.Join([]string{string(assignment.RoleVerification), model.NormalizeURL(evidence.URL),
				evidence.SpecID, authority.ReceiptID, authority.ReceiptDigest}, "\x00")
			if processedRoleEvidence[carrierKey] {
				continue
			}
			processedRoleEvidence[carrierKey] = true
			sourceInput, found := processEvidenceInputByID(inputs, assignmentProcessID)
			if !found {
				result.Index.Current = fmt.Sprintf("accepted verification receipt source PROCESS %s is unavailable", assignmentProcessID)
				return result
			}
			writer := strings.ToLower(strings.TrimSpace(authority.Provenance.Writer))
			independent := writer != "" && !sourceInput.AuthorAgentsBySpec[evidence.SpecID][writer]
			base := CanonicalEvidenceRecord{ProcessID: processID, SpecID: evidence.SpecID,
				Authority: CanonicalEvidenceRoleOwned, ReceiptID: authority.ReceiptID,
				ReceiptDigest: authority.ReceiptDigest, AssignmentProcessID: assignmentProcessID, AssignmentID: authority.AssignmentID,
				AssignmentDigest: authority.AssignmentDigest, AssignmentGeneration: authority.AssignmentGeneration,
				AssignmentRole:  assignment.RoleVerification,
				SubjectRevision: revision, URL: artifact.URL,
				Source: source, Trusted: true}
			targets := roleOwnedEvidenceTargets(inputs, assignmentProcessID, evidence.URL, evidence.SpecID,
				assignment.RoleVerification)
			verification := base
			verification.Kind, verification.EvidenceID = CanonicalEvidenceVerification, authority.ReceiptID
			addTargets(verification, targets, "", independent)
			for _, test := range authority.Tests {
				record := base
				record.Kind, record.EvidenceID = CanonicalEvidenceTest, authority.ReceiptID+":test:"+test.ID
				record.TestID = test.ID
				record.ResolvedRevision, record.ExecutedCommand = test.ResolvedRevision, test.Command
				if test.AssignedSelector != nil {
					selector := cloneFinalTestSelector(*test.AssignedSelector)
					record.AssignedSelector = &selector
				}
				addTargets(record, targets, test.ID, independent)
			}
			for _, check := range authority.Checks {
				record := base
				record.Kind, record.EvidenceID = CanonicalEvidenceCheck,
					authority.ReceiptID+":check:"+check.Provider+":"+check.Name
				selector := assignment.CheckSelector{Provider: check.Provider, Name: check.Name}
				record.CheckSelector = &selector
				addTargets(record, targets, check.Provider+"\x00"+check.Name, independent)
			}
		}
		for _, evidence := range input.Checks {
			if evidence.ProcessID != processID || evidence.SpecID == "" || !evidence.Required || !evidence.Passed ||
				!evidence.Trusted || evidence.SubjectRevision != revision {
				continue
			}
			source := canonicalFinalProviderSource(evidence.Source, evidence.Name)
			if source == "" {
				continue
			}
			addCovered(CanonicalEvidenceRecord{ProcessID: processID, SpecID: evidence.SpecID, Kind: CanonicalEvidenceCheck,
				Authority: CanonicalEvidenceProviderOwned, EvidenceID: source + ":" + evidence.Name,
				SubjectRevision: revision, Source: source, Trusted: true}, evidence.Name, true)
		}
		for _, evidence := range input.External {
			if evidence.ProcessID != processID || evidence.SpecID == "" || !evidence.Consumed || !evidence.Trusted ||
				evidence.SubjectRevision != revision || evidence.EvidenceRevision != revision {
				continue
			}
			kind := CanonicalEvidenceKind("")
			switch evidence.EvidenceKind {
			case string(codereview.EvidenceReview), "review_completion":
				kind = CanonicalEvidenceReview
			case string(codereview.EvidenceCheck):
				kind = CanonicalEvidenceCheck
			}
			if kind == "" {
				continue
			}
			for _, id := range evidence.EvidenceIDs {
				add(CanonicalEvidenceRecord{ProcessID: processID, SpecID: evidence.SpecID, Kind: kind,
					Authority: CanonicalEvidenceProviderOwned, EvidenceID: id, SubjectRevision: revision,
					Source: evidence.Source, Trusted: true}, id, true)
			}
		}
	}
	index, err := buildCanonicalEvidenceIndexForAssignments(records, revision, activeAssignments, MaxCanonicalEvidenceIndexEntries)
	result.Index.Passed = err == nil
	if err != nil {
		result.Index.Current = err.Error()
		return result
	}
	type indexKey struct {
		processID, specID string
		kind              CanonicalEvidenceKind
	}
	keys := map[indexKey]bool{}
	for _, record := range records {
		keys[indexKey{record.ProcessID, record.SpecID, record.Kind}] = true
	}
	for key := range keys {
		for _, record := range index.Records(key.processID, key.specID, key.kind) {
			meta := metadata[finalEvidenceMetadataKey(record)]
			result.Records = append(result.Records, gates.FinalEvidenceRecord{ProcessID: record.ProcessID,
				SpecID: record.SpecID, Kind: gates.FinalEvidenceKind(record.Kind), EvidenceID: record.EvidenceID,
				Name: meta.name, SubjectRevision: record.SubjectRevision, Source: record.Source,
				Independent: meta.independent, AssignmentProcessID: record.AssignmentProcessID,
				ReceiptID: record.ReceiptID, ReceiptDigest: record.ReceiptDigest, AssignmentID: record.AssignmentID,
				AssignmentDigest: record.AssignmentDigest, AssignmentGeneration: record.AssignmentGeneration,
				AssignmentRole:   record.AssignmentRole,
				AssignedSelector: record.AssignedSelector, ResolvedRevision: record.ResolvedRevision,
				ExecutedCommand: record.ExecutedCommand, CheckSelector: record.CheckSelector})
		}
	}
	sort.Slice(result.Records, func(i, j int) bool {
		left, right := result.Records[i], result.Records[j]
		if left.ProcessID != right.ProcessID {
			return left.ProcessID < right.ProcessID
		}
		if left.SpecID != right.SpecID {
			return left.SpecID < right.SpecID
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.EvidenceID < right.EvidenceID
	})
	return result
}

func acceptedRoleAssignmentProcess(inputs []gates.ProcessEvidenceInput, fallbackProcessID, evidenceURL, specID string,
	role assignment.Role, assignmentID, digest string, generation uint64, subject, declaredProcessID string) (string, error) {
	if declaredProcessID != "" {
		matches, exact := 0, 0
		declaredAuthority := false
		for _, input := range inputs {
			if input.Process.Comment.ID == declaredProcessID {
				matches++
			}
			if input.ActiveAssignment != nil &&
				acceptedAssignmentMatchesActive(input.ActiveAssignment, role, assignmentID, digest, generation, subject) {
				exact++
				declaredAuthority = declaredAuthority || input.Process.Comment.ID == declaredProcessID
			}
		}
		if matches != 1 {
			return "", fmt.Errorf("accepted %s receipt declares unavailable or duplicate issuing PROCESS %s", role, declaredProcessID)
		}
		if exact > 1 {
			return "", fmt.Errorf("accepted %s receipt has duplicate active assignment authority", role)
		}
		if !declaredAuthority {
			return "", fmt.Errorf("accepted %s receipt issuing PROCESS %s has no active assignment authority", role, declaredProcessID)
		}
		return declaredProcessID, nil
	}
	var candidates, exact []string
	hasManagedAssignment := false
	for _, input := range inputs {
		if input.ActiveAssignment != nil {
			hasManagedAssignment = true
			if acceptedAssignmentMatchesActive(input.ActiveAssignment, role, assignmentID, digest, generation, subject) {
				exact = append(exact, input.Process.Comment.ID)
			}
		}
		if !inputCarriesRoleEvidence(input, evidenceURL, specID, role, true) {
			continue
		}
		processID := input.Process.Comment.ID
		candidates = append(candidates, processID)
	}
	sort.Strings(candidates)
	sort.Strings(exact)
	if len(exact) > 1 {
		return "", fmt.Errorf("accepted %s receipt has duplicate active assignment authority on %s", role, strings.Join(exact, ", "))
	}
	if len(exact) == 1 {
		for _, candidate := range candidates {
			if candidate == exact[0] {
				return exact[0], nil
			}
		}
		return "", fmt.Errorf("accepted %s receipt does not reference its issuing role PROCESS %s", role, exact[0])
	}
	if len(candidates) > 1 {
		return "", fmt.Errorf("accepted %s receipt has ambiguous issuing role PROCESS candidates %s", role, strings.Join(candidates, ", "))
	}
	if len(candidates) == 1 {
		candidate := candidates[0]
		for _, input := range inputs {
			if input.Process.Comment.ID == candidate && input.ActiveAssignment == nil && hasManagedAssignment {
				return "", fmt.Errorf("accepted %s receipt issuing PROCESS %s has no active assignment authority", role, candidate)
			}
		}
		// Preserve the candidate even when its binding is historical or wrong.
		// The canonical index then makes an older generation ineligible and
		// rejects same/future-generation identity, role, digest, or subject drift.
		return candidate, nil
	}
	if !hasManagedAssignment {
		// Historical unmanaged single-carrier workflows have no role PROCESS
		// binding. Keep their existing literal receipt compatibility.
		return fallbackProcessID, nil
	}
	return "", fmt.Errorf("accepted %s receipt has no issuing role PROCESS with active assignment authority", role)
}

func processEvidenceInputByID(inputs []gates.ProcessEvidenceInput, processID string) (gates.ProcessEvidenceInput, bool) {
	for _, input := range inputs {
		if input.Process.Comment.ID == processID {
			return input, true
		}
	}
	return gates.ProcessEvidenceInput{}, false
}

func roleOwnedEvidenceTargets(inputs []gates.ProcessEvidenceInput, assignmentProcessID, evidenceURL, specID string,
	role assignment.Role) []string {
	targets := map[string]bool{assignmentProcessID: true}
	for _, input := range inputs {
		class := model.ParseProcessExecutionClass(input.Process.Comment.ID, input.Process.URL, input.Process.Comment.Body).Class
		if class != model.ProcessExecutionChangeBearing && class != model.ProcessExecutionExternal {
			continue
		}
		if inputCarriesRoleEvidence(input, evidenceURL, specID, role, false) {
			targets[input.Process.Comment.ID] = true
		}
	}
	result := make([]string, 0, len(targets))
	for processID := range targets {
		result = append(result, processID)
	}
	sort.Strings(result)
	return result
}

func inputCarriesRoleEvidence(input gates.ProcessEvidenceInput, evidenceURL, specID string, role assignment.Role,
	requireRoleClass bool) bool {
	if requireRoleClass {
		class := model.ParseProcessExecutionClass(input.Process.Comment.ID, input.Process.URL, input.Process.Comment.Body).Class
		expected := model.ProcessExecutionVerification
		if role == assignment.RoleReview {
			expected = model.ProcessExecutionReview
		}
		if class != expected {
			return false
		}
	}
	wantURL := model.NormalizeURL(evidenceURL)
	processID := input.Process.Comment.ID
	switch role {
	case assignment.RoleReview:
		for _, evidence := range input.Reviews {
			if evidence.ProcessID == processID && evidence.SpecID == specID && model.NormalizeURL(evidence.URL) == wantURL {
				return true
			}
		}
	case assignment.RoleVerification:
		for _, evidence := range input.Verifications {
			if evidence.ProcessID == processID && evidence.SpecID == specID && model.NormalizeURL(evidence.URL) == wantURL {
				return true
			}
		}
	}
	return false
}

// finalEvidenceProcessTargets keeps evidence attached to its canonical carrier
// and, only when the planning graph identifies one unambiguous code carrier for
// the same SPEC, projects a review/verification carrier's result onto that pair.
// Multi-carrier work therefore still requires explicit per-carrier evidence.
func finalEvidenceProcessTargets(artifacts []model.Artifact, input gates.ProcessEvidenceInput, specID string) []string {
	processID := input.Process.Comment.ID
	result := []string{processID}
	class := model.ParseProcessExecutionClass(processID, input.Process.URL, input.Process.Comment.Body).Class
	if class != model.ProcessExecutionReview && class != model.ProcessExecutionVerification {
		return result
	}
	specURL := input.ActiveSpecs[specID]
	var candidates []string
	for _, process := range activeProcessArtifacts(artifacts) {
		candidateClass := model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body).Class
		if candidateClass != model.ProcessExecutionChangeBearing && candidateClass != model.ProcessExecutionExternal {
			continue
		}
		if artifactReferencesSpec(process, specID, specURL) {
			candidates = append(candidates, process.Comment.ID)
		}
	}
	if len(candidates) == 1 && candidates[0] != processID {
		result = append(result, candidates[0])
	}
	sort.Strings(result)
	return result
}

func finalEvidenceMetadataKey(record CanonicalEvidenceRecord) string {
	return record.ProcessID + "\x00" + record.SpecID + "\x00" + string(record.Kind) + "\x00" + record.EvidenceID
}

func canonicalFinalProviderSource(source, fallback string) string {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "external-review-completion") {
		return "external-review-completion:" + strings.TrimSpace(fallback)
	}
	if canonicalProviderEvidenceSource(source) {
		return source
	}
	return ""
}

func exactAcceptedReviewCarrier(artifact model.Artifact, expectedRevision string) (acceptedReviewReceipt, bool) {
	if artifact.Comment.Type != "REVIEW" || artifact.Comment.Status != "done" || len(artifact.Comment.Errors) != 0 ||
		artifact.Comment.SubjectRevision != expectedRevision {
		return acceptedReviewReceipt{}, false
	}
	authority, found, err := parseAcceptedReviewReceipt(artifact.Comment.Body)
	if err != nil || !found || authority.SubjectRevision != expectedRevision || authority.Verdict != assignment.ReviewApprove {
		return acceptedReviewReceipt{}, false
	}
	identity, found, err := model.ObserveAcceptedReceiptAuthority(artifact.Comment.Body, assignment.RoleReview)
	if err != nil || !found || identity.ReceiptID != authority.ReceiptID || identity.Digest != authority.ReceiptDigest ||
		identity.Generation != authority.AssignmentGeneration || identity.AssignmentID != authority.AssignmentID ||
		identity.AssignmentDigest != authority.AssignmentDigest {
		return acceptedReviewReceipt{}, false
	}
	writer := strings.TrimSpace(authority.Provenance.Writer)
	if authority.Provenance.Route != assignment.RouteRoleOwned ||
		authority.Provenance.Assurance != assignment.AssuranceSelfReported || writer == "" ||
		!strings.EqualFold(writer, strings.TrimSpace(authority.Provenance.Subject)) ||
		!strings.EqualFold(writer, strings.TrimSpace(artifact.Comment.Agent)) || strings.EqualFold(writer, "Coordinator") {
		return acceptedReviewReceipt{}, false
	}
	seenTests := map[string]bool{}
	for _, test := range authority.Tests {
		if strings.TrimSpace(test.ID) == "" || strings.TrimSpace(test.Command) == "" || seenTests[test.ID] ||
			test.Outcome != assignment.TestPassed || test.Assurance != assignment.AssuranceSelfReported ||
			!exactAcceptedReviewTest(test, expectedRevision) {
			return acceptedReviewReceipt{}, false
		}
		seenTests[test.ID] = true
	}
	return authority, true
}

func exactAcceptedReviewTest(test acceptedReviewTest, expectedRevision string) bool {
	hasSelector := test.AssignedSelector != nil
	hasRevision := strings.TrimSpace(test.ResolvedRevision) != ""
	if hasSelector != hasRevision {
		return false
	}
	if !hasSelector {
		selector := assignment.TestSelector{ID: test.ID, Command: test.Command}
		return assignment.ValidateTestSelectorRevisionContract(assignment.RoleReview, expectedRevision, selector) == nil
	}
	if test.AssignedSelector.ID != test.ID ||
		assignment.ValidateTestSelectorRevisionContract(assignment.RoleReview, expectedRevision, *test.AssignedSelector) != nil {
		return false
	}
	resolved, err := assignment.ResolveTestSelector(*test.AssignedSelector, test.ResolvedRevision)
	return err == nil && strings.EqualFold(test.ResolvedRevision, expectedRevision) && test.Command == resolved.Command
}

func consumeAcceptedVerificationEvidence(inputs []gates.ProcessEvidenceInput, artifacts []model.Artifact,
	expectedRevision string) []gates.ProcessEvidenceInput {
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision == "" {
		return inputs
	}
	byURL := map[string]model.Artifact{}
	for _, artifact := range artifacts {
		if artifact.Comment.Type == "VERIFY" && artifact.Comment.Status == "done" {
			byURL[model.NormalizeURL(artifact.URL)] = artifact
		}
	}
	for inputIndex := range inputs {
		for evidenceIndex := range inputs[inputIndex].Verifications {
			evidence := &inputs[inputIndex].Verifications[evidenceIndex]
			artifact, ok := byURL[model.NormalizeURL(evidence.URL)]
			if !ok {
				continue
			}
			source, hasTests, hasChecks, ok := exactAcceptedVerificationCarrier(artifact, expectedRevision)
			if !ok {
				continue
			}
			authority, found, err := parseAcceptedVerificationReceipt(artifact.Comment.Body)
			if err != nil || !found {
				continue
			}
			assignmentProcessID, err := acceptedRoleAssignmentProcess(inputs, inputs[inputIndex].Process.Comment.ID,
				evidence.URL, evidence.SpecID, assignment.RoleVerification, authority.AssignmentID,
				authority.AssignmentDigest, authority.AssignmentGeneration, authority.SubjectRevision, "")
			if err != nil || assignmentProcessID != inputs[inputIndex].Process.Comment.ID ||
				!acceptedAssignmentMatchesActive(inputs[inputIndex].ActiveAssignment,
					assignment.RoleVerification, authority.AssignmentID, authority.AssignmentDigest,
					authority.AssignmentGeneration, authority.SubjectRevision) {
				continue
			}
			evidence.SubjectRevision = expectedRevision
			evidence.Trusted = true
			evidence.TestEvidence = true
			evidence.StructuredTests = hasTests
			evidence.StructuredChecks = hasChecks
			if hasTests {
				evidence.TestAssurance = string(assignment.AssuranceSelfReported)
			}
			evidence.Source = source
		}
	}
	return inputs
}

func acceptedAssignmentMatchesActive(active *gates.ActiveAssignmentEvidence, role assignment.Role,
	assignmentID, digest string, generation uint64, subject string) bool {
	// Unmanaged historical/manual workflows have no portable binding. Their
	// existing literal receipt behavior remains readable and eligible.
	if active == nil {
		return true
	}
	return active.Role == role && active.AssignmentID == assignmentID && active.AssignmentDigest == digest &&
		active.Generation == generation && active.SubjectRevision == subject
}

func exactAcceptedVerificationCarrier(artifact model.Artifact, expectedRevision string) (string, bool, bool, bool) {
	if artifact.Comment.Type != "VERIFY" || artifact.Comment.Status != "done" || len(artifact.Comment.Errors) != 0 ||
		!strings.EqualFold(strings.TrimSpace(artifact.Comment.SubjectRevision), strings.TrimSpace(expectedRevision)) {
		return "", false, false, false
	}
	authority, found, err := parseAcceptedVerificationReceipt(artifact.Comment.Body)
	if err != nil || !found || !strings.EqualFold(strings.TrimSpace(authority.SubjectRevision), strings.TrimSpace(expectedRevision)) {
		return "", false, false, false
	}
	identity, found, err := model.ObserveAcceptedReceiptAuthority(artifact.Comment.Body, assignment.RoleVerification)
	if err != nil || !found || identity.ReceiptID != authority.ReceiptID || identity.Digest != authority.ReceiptDigest ||
		identity.Generation != authority.AssignmentGeneration {
		return "", false, false, false
	}
	writer := strings.TrimSpace(authority.Provenance.Writer)
	if authority.Provenance.Route != assignment.RouteRoleOwned ||
		authority.Provenance.Assurance != assignment.AssuranceSelfReported || writer == "" ||
		!strings.EqualFold(writer, strings.TrimSpace(authority.Provenance.Subject)) || strings.EqualFold(writer, "Coordinator") {
		return "", false, false, false
	}
	if authority.Submission == nil || authority.Submission.Validate() != nil ||
		!strings.EqualFold(writer, authority.Submission.Agent) {
		return "", false, false, false
	}
	if len(authority.Tests) == 0 && len(authority.Checks) == 0 {
		return "", false, false, false
	}
	seenTests := map[string]bool{}
	for _, test := range authority.Tests {
		if strings.TrimSpace(test.ID) == "" || strings.TrimSpace(test.Command) == "" || seenTests[test.ID] ||
			test.Outcome != assignment.TestPassed || test.Assurance != assignment.AssuranceSelfReported {
			return "", false, false, false
		}
		if !exactAcceptedVerificationTest(test, expectedRevision) {
			return "", false, false, false
		}
		seenTests[test.ID] = true
	}
	seenChecks := map[string]bool{}
	for _, check := range authority.Checks {
		key := check.Provider + "\x00" + check.Name
		state := strings.ToLower(strings.TrimSpace(check.State))
		if strings.TrimSpace(check.Provider) == "" || strings.TrimSpace(check.Name) == "" ||
			strings.TrimSpace(check.EvidenceID) == "" || strings.TrimSpace(check.State) == "" || seenChecks[key] ||
			!strings.EqualFold(strings.TrimSpace(check.SubjectRevision), strings.TrimSpace(expectedRevision)) ||
			(!strings.HasPrefix(check.Source, "github-check-run:") && !strings.HasPrefix(check.Source, "native-evidence:")) {
			return "", false, false, false
		}
		if state != "success" && state != "neutral" && state != "skipped" && state != "passed" && state != "successful" {
			return "", false, false, false
		}
		if strings.HasPrefix(check.Source, "github-check-run:") && !strings.EqualFold(strings.TrimSpace(check.Provider), "github") {
			return "", false, false, false
		}
		seenChecks[key] = true
	}
	switch {
	case len(authority.Tests) > 0 && len(authority.Checks) > 0:
		return "accepted-verification-receipt:mixed-self-reported-tests-and-provider-checks", true, true, true
	case len(authority.Tests) > 0:
		return "accepted-verification-receipt:self-reported-tests", true, false, true
	default:
		return "accepted-verification-receipt:provider-checks", false, true, true
	}
}

func exactAcceptedVerificationTest(test acceptedVerificationTest, expectedRevision string) bool {
	hasSelector := test.AssignedSelector != nil
	hasRevision := strings.TrimSpace(test.ResolvedRevision) != ""
	if hasSelector != hasRevision {
		return false
	}
	if !hasSelector {
		selector := assignment.TestSelector{ID: test.ID, Command: test.Command}
		return assignment.ValidateTestSelectorRevisionContract(assignment.RoleVerification, expectedRevision, selector) == nil
	}
	if test.AssignedSelector.ID != test.ID ||
		assignment.ValidateTestSelectorRevisionContract(assignment.RoleVerification, expectedRevision, *test.AssignedSelector) != nil {
		return false
	}
	resolved, err := assignment.ResolveTestSelector(*test.AssignedSelector, test.ResolvedRevision)
	return err == nil && strings.EqualFold(test.ResolvedRevision, expectedRevision) && test.Command == resolved.Command
}

func mergeCarrierRevisionFacts(collected, supplied map[string]gates.CarrierRevisionFact) map[string]gates.CarrierRevisionFact {
	merged := make(map[string]gates.CarrierRevisionFact, len(collected)+len(supplied))
	for processID, fact := range collected {
		merged[processID] = fact
	}
	for processID, fact := range supplied {
		merged[processID] = fact
	}
	return merged
}

func legacyVerifyGateError(diagnostic gates.Diagnostic) (string, bool) {
	id := diagnostic.Artifact.ID
	switch diagnostic.Code {
	case gates.CodeSpecRequired:
		return "at least one active SPEC is required", true
	case gates.CodeTaskRequired:
		return "at least one active TASK is required", true
	case gates.CodeProcessRequired:
		return "at least one active PROCESS is required", true
	case gates.CodeSpecStatusInvalid:
		return fmt.Sprintf("%s must be confirmed or done before final verify", id), true
	case gates.CodeQuestionBlocked:
		return fmt.Sprintf("%s is still blocked", id), true
	case gates.CodeTaskNotDone:
		return fmt.Sprintf("%s must be done before final verify", id), true
	case gates.CodeProcessNotDone:
		return fmt.Sprintf("%s must be done before final verify", id), true
	case gates.CodeReviewOpen:
		return fmt.Sprintf("%s must be done or superseded before final verify", id), true
	case gates.CodeVerifyRequired:
		return "at least one done VERIFY comment is required", true
	case gates.CodeVerifyTestEvidenceMissing:
		return "no done VERIFY comment references test evidence (SPEC-006)", true
	case gates.CodeVerifySpecCoverageMissing:
		return fmt.Sprintf("%s is not referenced by any done VERIFY comment", id), true
	case gates.CodeProcessHandoffMissing:
		return fmt.Sprintf("%s is a serial-chain predecessor but records no ### Handoff evidence (SPEC-006)", id), true
	case gates.CodeArtifactNoncanonical:
		url := diagnostic.Artifact.URL
		if url == "" {
			url = "N/A"
		}
		return fmt.Sprintf("%s %s (%s) is noncanonical: %s", diagnostic.Artifact.Type, id, url, diagnostic.Message), true
	case gates.CodeTraceabilityInvalid:
		return diagnostic.Message, true
	case gates.CodeProviderEvidenceMissing:
		if current := strings.TrimSpace(diagnostic.Current); current != "" && current != "failed" {
			return "verify external evidence: " + current, true
		}
		return diagnostic.Message, true
	case gates.CodeProviderEvidenceUnknown:
		return diagnostic.Message, true
	case gates.CodeVerifyRevisionInvalid:
		if current := strings.TrimSpace(diagnostic.Current); current != "" && current != "failed" {
			return current, true
		}
		return diagnostic.Message, true
	case gates.CodeVerifyRevisionUnknown:
		return diagnostic.Message, true
	case gates.CodeProcessExecutionClassInvalid, gates.CodeProcessTaskLinkMissing,
		gates.CodeProcessSpecLinkMissing, gates.CodeProcessPRLinkMissing, gates.CodeProcessCarrierMissing,
		gates.CodeProcessExecutorCoordinatorConflict, gates.CodeProcessReviewRequired, gates.CodeProcessReviewAuthorConflict:
		return diagnostic.Message, true
	case gates.CodeProcessWorkspaceRequired, gates.CodeProcessWorkspaceInvalid, gates.CodeProcessWorkspaceStateInvalid,
		gates.CodeProcessWorkspaceModeInvalid, gates.CodeProcessWorkspaceRevisionUnknown, gates.CodeProcessWorkspaceRevisionStale,
		gates.CodeProcessWorkspaceReviewEvidenceMissing, gates.CodeProcessWorkspaceVerifyEvidenceMissing,
		gates.CodeProcessWorkspaceProviderEvidenceMissing:
		return diagnostic.Message, true
	default:
		// Remote check/finding diagnostics have richer legacy projections below;
		// PROCESS evidence policy is integrated by PROCESS-009.
		return "", false
	}
}

func hasExplicitProcessWorkspace(artifacts []model.Artifact) bool {
	for _, artifact := range activeProcessArtifacts(artifacts) {
		if model.ParseProcessWorkspace(artifact.Comment.ID, artifact.URL, artifact.Comment.Body).Explicit {
			return true
		}
	}
	return false
}

// sectionContent returns the trimmed text of the named `###`/`##` section, up to
// the next heading of the same or higher level.
func sectionContent(body, heading string) string {
	lines := strings.Split(model.LogicalBody(body), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	var out []string
	for _, line := range lines[start:] {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") || strings.HasPrefix(strings.TrimSpace(line), "### ") {
			break
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isEmptyOrNA(text string) bool {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "-"))
	return text == "" || strings.EqualFold(text, "N/A")
}

func linkValuesContain(values []string, want string) bool {
	want = model.NormalizeURL(want)
	for _, value := range values {
		if model.NormalizeURL(value) == want {
			return true
		}
	}
	return false
}

func rationaleCoverage(comments []github.PullRequestReviewComment, activeSpecIDs map[string]bool) map[string]bool {
	covered := map[string]bool{}
	for _, comment := range comments {
		marker, ok, err := model.FindRationaleMarker(comment.Body)
		if err != nil || !ok {
			continue
		}
		if marker.Process == "" || marker.Spec == "" || !activeSpecIDs[marker.Spec] {
			continue
		}
		if !strings.Contains(comment.Body, "Spec Comment:") {
			continue
		}
		covered[marker.Process] = true
	}
	return covered
}

func printFinalVerify(out interface{ Write([]byte) (int, error) }, report finalVerifyReport) {
	if report.OK {
		fmt.Fprintln(out, "final verify OK")
	} else {
		fmt.Fprintln(out, "final verify failed")
	}
	fmt.Fprintf(out, "traceability: %v\n", report.Traceability.OK)
	for specID, covered := range report.SpecCoverage {
		fmt.Fprintf(out, "coverage %s: %v\n", specID, covered)
	}
	for processID, covered := range report.RationaleCoverage {
		fmt.Fprintf(out, "rationale %s: %v\n", processID, covered)
	}
	for _, process := range report.ProcessEvidence {
		fmt.Fprintf(out, "process evidence: %s\n", process.Summary())
	}
	for _, err := range report.Errors {
		fmt.Fprintf(out, "- %s\n", err)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "- warning: %s\n", warning)
	}
	if len(report.Diagnostics) > 0 {
		fmt.Fprintln(out, "metadata diagnostics:")
		for _, diagnostic := range report.Diagnostics {
			fmt.Fprintf(out, "- %s %s: %s\n", diagnostic.Level, diagnostic.Code, diagnostic.Message)
		}
	}
}
