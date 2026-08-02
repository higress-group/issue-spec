package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type nativeCodeChangeBackend interface {
	ResolveNativeIssue(context.Context, string, int) (models.RepoScope, uuid.UUID, error)
	GetNativeActiveBinding(context.Context, models.RepoScope) (github.NativeBinding, error)
	ListNativeReferences(context.Context, models.RepoScope, uuid.UUID) ([]github.NativeReference, error)
	UpsertNativeReference(context.Context, models.RepoScope, uuid.UUID, github.NativeUpsertReferenceInput) (github.NativeReference, error)
	CompatibilityIssueBackend() github.IssueBackend
}

type commandNativeCodeChangeBackend struct {
	native        *github.Client
	compatibility github.IssueBackend
}

func defaultNewNativeCodeChangeBackend(profile auth.Profile, token string) (nativeCodeChangeBackend, error) {
	native, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL,
		Token: token, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	compatibility, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.APIURL,
		Token: token, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	return &commandNativeCodeChangeBackend{native: native, compatibility: compatibility}, nil
}

func (b *commandNativeCodeChangeBackend) ResolveNativeIssue(ctx context.Context, repository string, issueNumber int) (models.RepoScope, uuid.UUID, error) {
	parsed, err := github.ParseRepo(repository)
	if err != nil || issueNumber <= 0 {
		return models.RepoScope{}, uuid.Nil, errors.New("invalid repository or Implement Issue")
	}
	parts := strings.Split(parsed, "/")
	owner, ownerErr := url.PathUnescape(parts[0])
	repo, repoErr := url.PathUnescape(parts[1])
	if ownerErr != nil || repoErr != nil {
		return models.RepoScope{}, uuid.Nil, errors.New("invalid repository identity")
	}
	current, err := b.native.GetNativeContext(ctx)
	if err != nil {
		return models.RepoScope{}, uuid.Nil, err
	}
	organization, err := selectNativeOrganization(current.Organizations, owner)
	if err != nil {
		return models.RepoScope{}, uuid.Nil, err
	}
	repositories, err := b.native.ListNativeContextRepositories(ctx, organization.ID)
	if err != nil {
		return models.RepoScope{}, uuid.Nil, err
	}
	selected, exists, err := selectNativeRepository(repositories.Repositories, repo)
	if err != nil {
		return models.RepoScope{}, uuid.Nil, err
	}
	if !exists {
		return models.RepoScope{}, uuid.Nil, fmt.Errorf("repository %q is unavailable", repository)
	}
	scope, err := repositoryScope(organization.ID, selected.Repository.ID)
	if err != nil {
		return models.RepoScope{}, uuid.Nil, err
	}
	issue, err := b.compatibility.GetIssue(ctx, parsed, issueNumber)
	if err != nil {
		return models.RepoScope{}, uuid.Nil, err
	}
	if issue.Number != issueNumber || issue.PullRequest != nil ||
		!strings.HasPrefix(issue.Body, "<!-- issue-spec:issue=implement ") {
		return models.RepoScope{}, uuid.Nil, errors.New("Implement Issue response is incomplete or mismatched")
	}
	issueID, err := nativeIssueNodeID(issue.NodeID)
	if err != nil {
		return models.RepoScope{}, uuid.Nil, err
	}
	return scope, issueID, nil
}

func (b *commandNativeCodeChangeBackend) GetNativeActiveBinding(ctx context.Context, scope models.RepoScope) (github.NativeBinding, error) {
	return b.native.GetNativeActiveBinding(ctx, scope)
}

func (b *commandNativeCodeChangeBackend) ListNativeReferences(ctx context.Context, scope models.RepoScope,
	issueID uuid.UUID) ([]github.NativeReference, error) {
	return b.native.ListNativeReferences(ctx, scope, issueID)
}

func (b *commandNativeCodeChangeBackend) UpsertNativeReference(ctx context.Context, scope models.RepoScope, issueID uuid.UUID,
	input github.NativeUpsertReferenceInput) (github.NativeReference, error) {
	return b.native.UpsertNativeReference(ctx, scope, issueID, input)
}

func (b *commandNativeCodeChangeBackend) CompatibilityIssueBackend() github.IssueBackend {
	return b.compatibility
}

func nativeIssueNodeID(value string) (uuid.UUID, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || !strings.HasPrefix(string(decoded), "Issue:") {
		return uuid.Nil, errors.New("Implement Issue has an invalid native node_id")
	}
	id, err := uuid.Parse(strings.TrimPrefix(string(decoded), "Issue:"))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("Implement Issue has an invalid native node_id")
	}
	return id, nil
}

func (a *app) runCodeChange(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec code-change attach|link-process|merge ...\n")
		return 2
	}
	switch args[0] {
	case "attach":
		return a.runCodeChangeAttach(ctx, args[1:])
	case "link-process":
		return a.runCodeChangeLinkProcess(ctx, args[1:])
	case "rationale":
		return a.outputDeprecatedWorkflow(deprecatedResultFor("code-change rationale", args[1:], "provider-native discussion"), append([]string{"code-change", "rationale"}, args[1:]...))
	case "merge":
		return a.runCodeChangeMerge(ctx, args[1:])
	default:
		a.errorf("unknown code-change command %q\n", args[0])
		return 2
	}
}

type codeChangeAttachResult struct {
	OK                    bool   `json:"ok"`
	Action                string `json:"action"`
	Repo                  string `json:"repo"`
	Implement             int    `json:"implement"`
	ReferenceID           string `json:"reference_id"`
	ProviderKey           string `json:"provider_key"`
	ExternalRepository    string `json:"external_repository"`
	ChangeID              string `json:"change_id"`
	Revision              string `json:"revision"`
	CanonicalURL          string `json:"canonical_url"`
	RepresentationVersion int64  `json:"representation_version"`
	RefreshRequested      bool   `json:"refresh_requested"`
}

type codeChangeAttachErrorResult struct {
	OK         bool                                  `json:"ok"`
	Code       string                                `json:"code"`
	Message    string                                `json:"message"`
	Reason     github.NativeCodeChangeConflictReason `json:"reason,omitempty"`
	References []github.NativeReferenceIdentity      `json:"references,omitempty"`
	RequestID  string                                `json:"request_id,omitempty"`
}

func (a *app) runCodeChangeAttach(ctx context.Context, args []string) int {
	fs := newFlagSet("code-change attach", a.err)
	repoFlag := fs.String("repo", "", "self-hosted repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	implementFlag := fs.String("implement", "", "Implement Issue number or URL")
	changeID := fs.String("change-id", "", "existing provider-owned opaque change identifier")
	revision := fs.String("revision", "", "exact provider head revision")
	refresh := fs.Bool("refresh", false, "refresh the same active change to a new exact revision")
	expectedVersion := fs.Int64("expected-version", 0, "caller-observed active reference representation version")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if _, ok := a.validateRepo(*repoFlag); !ok {
		return 2
	}
	repository := strings.TrimSpace(*repoFlag)
	implement, err := parseIssueFlag(*implementFlag, "implement")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	*changeID = strings.TrimSpace(*changeID)
	*revision = strings.TrimSpace(*revision)
	if *changeID == "" {
		a.errorf("--change-id is required\n")
		return 2
	}
	if *revision == "" {
		a.errorf("--revision is required\n")
		return 2
	}
	if *refresh != (*expectedVersion > 0) {
		a.errorf("--refresh and a positive --expected-version must be provided together\n")
		return 2
	}

	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		return a.codeChangeAttachError(*jsonOut, "profile_unavailable", "resolve issue backend profile", err, nil)
	}
	if profile.Kind != auth.ProfileKindHosted {
		return a.codeChangeAttachError(*jsonOut, "self_hosted_required", "code-change attach requires a self-hosted profile", nil, nil)
	}
	token, err := auth.ResolveProfileToken(ctx, profile)
	if err != nil {
		return a.codeChangeAttachError(*jsonOut, "auth_required", "resolve self-hosted profile credential", err, nil)
	}
	if a.newNativeCodeChangeBackend == nil {
		return a.codeChangeAttachError(*jsonOut, "native_backend_unavailable", "configure native code-change backend", errors.New("backend is unavailable"), nil)
	}
	backend, err := a.newNativeCodeChangeBackend(profile, token.Value)
	if err != nil {
		return a.codeChangeAttachError(*jsonOut, "native_backend_unavailable", "configure native code-change backend", err, nil)
	}
	scope, issueID, err := backend.ResolveNativeIssue(ctx, repository, implement)
	if err != nil {
		return a.codeChangeAttachError(*jsonOut, "implement_unavailable", "resolve Implement Issue", err, nil)
	}
	binding, err := backend.GetNativeActiveBinding(ctx, scope)
	if err != nil {
		return a.codeChangeAttachError(*jsonOut, "source_binding_unavailable", "read active Source Binding", err, nil)
	}
	reference := codereview.Reference{ProviderKey: binding.ProviderKey,
		ExternalRepository: binding.ExternalRepositoryID, ChangeID: *changeID}
	if err := reference.Validate(); err != nil {
		return a.codeChangeAttachError(*jsonOut, "invalid_change_identity", "construct binding-authoritative change identity", err, nil)
	}
	provider, err := a.resolveOperatorProvider(ctx, profile, binding.ProviderKey)
	if err != nil {
		return a.codeChangeAttachError(*jsonOut, "provider_unavailable", "resolve operator code provider", err, nil)
	}
	navigation, err := codereview.ResolveNavigationChange(ctx, provider, codereview.SnapshotRequest{
		Reference: reference, SubjectRevision: *revision,
	})
	if err != nil {
		code := "provider_snapshot_failed"
		switch {
		case errors.Is(err, codereview.ErrCapabilityMissing):
			code = "provider_capability_missing"
		case errors.Is(err, codereview.ErrInvalidProviderData):
			code = "provider_data_invalid"
		}
		return a.codeChangeAttachError(*jsonOut, code, "validate existing code change", err, nil)
	}
	metadata, err := json.Marshal(map[string]string{"head_revision": navigation.SubjectRevision})
	if err != nil {
		return a.codeChangeAttachError(*jsonOut, "reference_input_invalid", "encode code-change relationship", err, nil)
	}
	var expected *int64
	if *refresh {
		expected = expectedVersion
	}
	attached, err := backend.UpsertNativeReference(ctx, scope, issueID, github.NativeUpsertReferenceInput{
		ProviderKey: navigation.Reference.ProviderKey, RelationKind: "code_change",
		ExternalRepositoryID: navigation.Reference.ExternalRepository, ExternalID: navigation.Reference.ChangeID,
		CanonicalURL: navigation.CanonicalURL, LifecycleState: "active", Visibility: "repository", Metadata: metadata,
		Refresh: *refresh, ExpectedVersion: expected,
	})
	if err != nil {
		var conflict *github.NativeCodeChangeConflictError
		if errors.As(err, &conflict) {
			return a.codeChangeAttachError(*jsonOut, "code_change_conflict", "establish active code-change relationship", conflict, conflict)
		}
		return a.codeChangeAttachError(*jsonOut, "reference_upsert_failed", "establish active code-change relationship", err, nil)
	}
	result := codeChangeAttachResult{OK: true, Action: "attached", Repo: repository, Implement: implement,
		ReferenceID: attached.ID, ProviderKey: attached.ProviderKey, ExternalRepository: attached.ExternalRepositoryID,
		ChangeID: attached.ExternalID, Revision: navigation.SubjectRevision, CanonicalURL: attached.CanonicalURL,
		RepresentationVersion: attached.RepresentationVersion, RefreshRequested: *refresh}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "attached code change %s at %s to %s#%d (%s, reference version %d)\n",
		result.ChangeID, result.Revision, result.Repo, result.Implement, result.CanonicalURL, result.RepresentationVersion)
	return 0
}

func (a *app) codeChangeAttachError(jsonOut bool, code, operation string, err error, conflict *github.NativeCodeChangeConflictError) int {
	message := operation
	if err != nil {
		message += ": " + err.Error()
	}
	if jsonOut {
		result := codeChangeAttachErrorResult{OK: false, Code: code, Message: message}
		if conflict != nil {
			result.Reason = conflict.Reason
			result.References = append([]github.NativeReferenceIdentity(nil), conflict.References...)
			result.RequestID = conflict.RequestID
		}
		_ = a.outputJSON(result)
		return 1
	}
	a.errorf("%s\n", message)
	return 1
}

var (
	errActiveCodeChangeMissing   = errors.New("Implement Issue has no active code_change relationship")
	errActiveCodeChangeAmbiguous = errors.New("Implement Issue has multiple active code_change relationships")
)

type codeChangeLinkProcessResult struct {
	OK                    bool                            `json:"ok"`
	Action                string                          `json:"action"`
	Repo                  string                          `json:"repo"`
	Implement             int                             `json:"implement"`
	Process               string                          `json:"process"`
	CommentID             int64                           `json:"comment_id"`
	CommentURL            string                          `json:"comment_url,omitempty"`
	CanonicalURL          string                          `json:"canonical_url"`
	Guarantee             github.CommentMutationGuarantee `json:"guarantee"`
	Atomic                bool                            `json:"atomic"`
	ExpectedVersion       int64                           `json:"expected_version"`
	RepresentationVersion int64                           `json:"representation_version"`
}

type codeChangeLinkProcessErrorResult struct {
	OK       bool   `json:"ok"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Expected int64  `json:"expected,omitempty"`
	Current  int64  `json:"current,omitempty"`
}

func (a *app) runCodeChangeLinkProcess(ctx context.Context, args []string) int {
	fs := newFlagSet("code-change link-process", a.err)
	repoFlag := fs.String("repo", "", "self-hosted repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	implementFlag := fs.String("implement", "", "Implement Issue number or URL")
	processID := fs.String("process", "", "PROCESS id on the Implement Issue")
	expectedVersion := fs.Int64("expected-version", 0, "caller-observed PROCESS comment representation version")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if _, ok := a.validateRepo(*repoFlag); !ok {
		return 2
	}
	repository := strings.TrimSpace(*repoFlag)
	implement, err := parseIssueFlag(*implementFlag, "implement")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	*processID = strings.TrimSpace(*processID)
	if *processID == "" {
		a.errorf("--process is required\n")
		return 2
	}
	if *expectedVersion <= 0 {
		a.errorf("--expected-version must be positive\n")
		return 2
	}

	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		return a.codeChangeLinkProcessError(*jsonOut, "profile_unavailable", "resolve issue backend profile", err, nil)
	}
	if profile.Kind != auth.ProfileKindHosted {
		return a.codeChangeLinkProcessError(*jsonOut, "self_hosted_required", "code-change link-process requires a self-hosted profile", nil, nil)
	}
	token, err := auth.ResolveProfileToken(ctx, profile)
	if err != nil {
		return a.codeChangeLinkProcessError(*jsonOut, "auth_required", "resolve self-hosted profile credential", err, nil)
	}
	if a.newNativeCodeChangeBackend == nil {
		return a.codeChangeLinkProcessError(*jsonOut, "native_backend_unavailable", "configure native code-change backend",
			errors.New("backend is unavailable"), nil)
	}
	backend, err := a.newNativeCodeChangeBackend(profile, token.Value)
	if err != nil {
		return a.codeChangeLinkProcessError(*jsonOut, "native_backend_unavailable", "configure native code-change backend", err, nil)
	}
	scope, issueID, err := backend.ResolveNativeIssue(ctx, repository, implement)
	if err != nil {
		return a.codeChangeLinkProcessError(*jsonOut, "implement_unavailable", "resolve Implement Issue", err, nil)
	}
	references, err := backend.ListNativeReferences(ctx, scope, issueID)
	if err != nil {
		return a.codeChangeLinkProcessError(*jsonOut, "reference_read_failed", "read Implement Issue references", err, nil)
	}
	canonicalURL, err := uniqueActiveCodeChangeURL(references)
	if err != nil {
		code := "active_code_change_invalid"
		switch {
		case errors.Is(err, errActiveCodeChangeMissing):
			code = "active_code_change_missing"
		case errors.Is(err, errActiveCodeChangeAmbiguous):
			code = "active_code_change_ambiguous"
		}
		return a.codeChangeLinkProcessError(*jsonOut, code, "resolve active code-change relationship", err, nil)
	}
	issueBackend := backend.CompatibilityIssueBackend()
	if issueBackend == nil {
		return a.codeChangeLinkProcessError(*jsonOut, "issue_backend_unavailable", "configure Implement Issue backend",
			errors.New("backend is unavailable"), nil)
	}
	artifact, _, err := findUniqueTransitionArtifactByID(ctx, issueBackend, repository, implement, *processID)
	if err != nil {
		return a.codeChangeLinkProcessError(*jsonOut, "process_unavailable", "resolve unique PROCESS typed comment", err, nil)
	}
	if artifact.Comment.Type != "PROCESS" {
		return a.codeChangeLinkProcessError(*jsonOut, "process_invalid", "validate PROCESS typed comment",
			fmt.Errorf("%s is %s, not PROCESS", *processID, artifact.Comment.Type), nil)
	}
	conditional, err := github.RequireConditionalCommentBackend(issueBackend)
	if err != nil {
		return a.codeChangeLinkProcessError(*jsonOut, "conditional_comment_required",
			"code-change link-process requires representation-version CAS", err, nil)
	}
	observed, err := conditional.GetCommentRepresentation(ctx, repository, artifact.CommentID)
	if err != nil {
		return a.codeChangeLinkProcessError(*jsonOut, "comment_observation_failed", "observe PROCESS comment representation", err, nil)
	}
	if observed.Comment.ID != artifact.CommentID || observed.RepresentationVersion <= 0 {
		return a.codeChangeLinkProcessError(*jsonOut, "comment_observation_invalid",
			"observe PROCESS comment representation", errors.New("response identity is incomplete or mismatched"), nil)
	}
	if observed.RepresentationVersion != *expectedVersion {
		return a.codeChangeLinkProcessError(*jsonOut, "comment_representation_conflict", "PROCESS comment representation is stale", nil,
			&github.CommentMutationConflictError{Expected: *expectedVersion, Current: observed.RepresentationVersion})
	}
	updatedBody, changed, err := model.SetProcessCodeChangeLink(observed.Comment.Body, *processID, canonicalURL)
	if err != nil {
		code := "process_mutation_invalid"
		if errors.Is(err, model.ErrProcessPRLinkConflict) {
			code = "process_pr_link_conflict"
		}
		return a.codeChangeLinkProcessError(*jsonOut, code, "set PROCESS code-change link", err, nil)
	}
	result := codeChangeLinkProcessResult{OK: true, Action: "unchanged", Repo: repository, Implement: implement,
		Process: *processID, CommentID: artifact.CommentID, CommentURL: artifact.URL, CanonicalURL: canonicalURL,
		Guarantee: github.CommentMutationStrictConditional, Atomic: true, ExpectedVersion: *expectedVersion,
		RepresentationVersion: observed.RepresentationVersion}
	if changed {
		updated, err := conditional.UpdateCommentConditional(ctx, repository, artifact.CommentID, observed.RepresentationVersion, updatedBody)
		if err != nil {
			var conflict *github.CommentMutationConflictError
			if errors.As(err, &conflict) {
				return a.codeChangeLinkProcessError(*jsonOut, "comment_representation_conflict", "PROCESS comment changed concurrently", nil, conflict)
			}
			return a.codeChangeLinkProcessError(*jsonOut, "comment_update_failed", "patch PROCESS code-change link", err, nil)
		}
		if updated.Comment.ID != artifact.CommentID || updated.Comment.Body != updatedBody ||
			updated.RepresentationVersion <= observed.RepresentationVersion {
			return a.codeChangeLinkProcessError(*jsonOut, "comment_update_invalid", "patch PROCESS code-change link",
				errors.New("response identity, body, or representation version is incomplete or mismatched"), nil)
		}
		result.Action = "updated"
		result.RepresentationVersion = updated.RepresentationVersion
		if updated.Comment.HTMLURL != "" {
			result.CommentURL = updated.Comment.HTMLURL
		}
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s %s to code change %s (comment version %d)\n", result.Action, result.Process,
		result.CanonicalURL, result.RepresentationVersion)
	return 0
}

func uniqueActiveCodeChangeURL(references []github.NativeReference) (string, error) {
	matches := make([]github.NativeReference, 0, 1)
	for _, reference := range references {
		if reference.RelationKind == "code_change" && reference.LifecycleState == "active" {
			matches = append(matches, reference)
		}
	}
	if len(matches) == 0 {
		return "", errActiveCodeChangeMissing
	}
	if len(matches) != 1 {
		return "", errActiveCodeChangeAmbiguous
	}
	if strings.TrimSpace(matches[0].CanonicalURL) == "" {
		return "", errors.New("active code_change relationship has no canonical URL")
	}
	return matches[0].CanonicalURL, nil
}

func uniqueActiveCodeChangeIdentity(references []github.NativeReference) (github.NativeReference, string, error) {
	matches := make([]github.NativeReference, 0, 1)
	for _, reference := range references {
		if reference.RelationKind == "code_change" && reference.LifecycleState == "active" {
			matches = append(matches, reference)
		}
	}
	if len(matches) == 0 {
		return github.NativeReference{}, "", errActiveCodeChangeMissing
	}
	if len(matches) != 1 {
		return github.NativeReference{}, "", errActiveCodeChangeAmbiguous
	}
	reference := matches[0]
	if strings.TrimSpace(reference.ProviderKey) == "" || strings.TrimSpace(reference.ExternalRepositoryID) == "" ||
		strings.TrimSpace(reference.ExternalID) == "" || strings.TrimSpace(reference.CanonicalURL) == "" || reference.RepresentationVersion <= 0 {
		return github.NativeReference{}, "", errors.New("active code_change relationship identity is incomplete")
	}
	var metadata struct {
		HeadRevision string `json:"head_revision"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(reference.Metadata)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil || strings.TrimSpace(metadata.HeadRevision) == "" {
		return github.NativeReference{}, "", errors.New("active code_change relationship revision is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return github.NativeReference{}, "", errors.New("active code_change relationship revision is invalid")
	}
	return reference, strings.TrimSpace(metadata.HeadRevision), nil
}

func (a *app) codeChangeLinkProcessError(jsonOut bool, code, operation string, err error,
	conflict *github.CommentMutationConflictError) int {
	message := operation
	if err != nil {
		message += ": " + err.Error()
	}
	if jsonOut {
		result := codeChangeLinkProcessErrorResult{OK: false, Code: code, Message: message}
		if conflict != nil {
			result.Expected, result.Current = conflict.Expected, conflict.Current
		}
		_ = a.outputJSON(result)
		return 1
	}
	if conflict != nil {
		message += fmt.Sprintf(": expected=%d current=%d", conflict.Expected, conflict.Current)
	}
	a.errorf("%s\n", message)
	return 1
}
