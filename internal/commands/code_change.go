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
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/workflow"
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
		a.errorf("usage: issue-spec code-change attach|link-process|rationale ...\n")
		return 2
	}
	switch args[0] {
	case "attach":
		return a.runCodeChangeAttach(ctx, args[1:])
	case "link-process":
		return a.runCodeChangeLinkProcess(ctx, args[1:])
	case "rationale":
		return a.runCodeChangeRationale(ctx, args[1:])
	default:
		a.errorf("unknown code-change command %q\n", args[0])
		return 2
	}
}

type codeChangeRationaleResult struct {
	OK                    bool   `json:"ok"`
	Created               bool   `json:"created"`
	Already               bool   `json:"already"`
	Repo                  string `json:"repo"`
	Implement             int    `json:"implement"`
	CommentID             int64  `json:"comment_id,omitempty"`
	CommentURL            string `json:"comment_url,omitempty"`
	Process               string `json:"process"`
	Spec                  string `json:"spec"`
	ProviderKey           string `json:"provider_key"`
	ExternalRepository    string `json:"external_repository"`
	ChangeID              string `json:"change_id"`
	SubjectRevision       string `json:"subject_revision"`
	RepresentationVersion int64  `json:"representation_version"`
	PublicationState      string `json:"publication_state"`
	RationaleID           string `json:"rationale_id,omitempty"`
	ExternalCommentID     string `json:"external_comment_id,omitempty"`
	ExternalCommentURL    string `json:"external_comment_url,omitempty"`
	ExternalCapability    string `json:"external_capability,omitempty"`
}

func (a *app) runCodeChangeRationale(ctx context.Context, args []string) int {
	fs := newFlagSet("code-change rationale", a.err)
	repoFlag := fs.String("repo", "", "self-hosted repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	implementFlag := fs.String("implement", "", "Implement Issue number or URL")
	processID := fs.String("process", "", "change-bearing PROCESS id on the Implement Issue")
	specID := fs.String("spec", "", "active SPEC id covered by the PROCESS")
	specURL := fs.String("spec-url", "", "active SPEC comment URL")
	bodyFile := fs.String("body-file", "", "rationale body file, or - for stdin")
	bodyText := fs.String("body", "", "rationale body text")
	agent := fs.String("agent", "Worker Agent", "logical code-author agent identity")
	_ = addAgentSessionFlag(fs)
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
	*processID, *specID, *specURL, *agent = strings.TrimSpace(*processID), strings.TrimSpace(*specID), strings.TrimSpace(*specURL), strings.TrimSpace(*agent)
	if *processID == "" || *specID == "" || *specURL == "" || *agent == "" {
		a.errorf("--process, --spec, --spec-url, and --agent are required\n")
		return 2
	}
	body := strings.TrimSpace(*bodyText)
	if *bodyFile != "" {
		content, ok := a.readBodyFile(*bodyFile)
		if !ok {
			return 2
		}
		body = strings.TrimSpace(content)
	}
	if body == "" {
		a.errorf("--body or --body-file is required\n")
		return 2
	}
	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "profile_unavailable", "resolve issue backend profile", err)
	}
	if profile.Kind != auth.ProfileKindHosted {
		return a.codeChangeRationaleError(*jsonOut, "self_hosted_required", "code-change rationale requires a self-hosted profile", nil)
	}
	token, err := auth.ResolveProfileToken(ctx, profile)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "auth_required", "resolve self-hosted profile credential", err)
	}
	if a.newNativeCodeChangeBackend == nil {
		return a.codeChangeRationaleError(*jsonOut, "native_backend_unavailable", "configure native code-change backend", errors.New("backend is unavailable"))
	}
	backend, err := a.newNativeCodeChangeBackend(profile, token.Value)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "native_backend_unavailable", "configure native code-change backend", err)
	}
	scope, issueID, err := backend.ResolveNativeIssue(ctx, repository, implement)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "implement_unavailable", "resolve Implement Issue", err)
	}
	references, err := backend.ListNativeReferences(ctx, scope, issueID)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "reference_read_failed", "read Implement Issue references", err)
	}
	reference, revision, err := uniqueActiveCodeChangeIdentity(references)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "active_code_change_invalid", "resolve active code-change relationship", err)
	}
	issueBackend := backend.CompatibilityIssueBackend()
	if issueBackend == nil {
		return a.codeChangeRationaleError(*jsonOut, "issue_backend_unavailable", "configure Implement Issue backend", errors.New("backend is unavailable"))
	}
	process, _, err := findUniqueTransitionArtifactByID(ctx, issueBackend, repository, implement, *processID)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "process_unavailable", "resolve unique PROCESS typed comment", err)
	}
	if process.Comment.Type != "PROCESS" || process.Comment.Status == "superseded" ||
		model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body).Class != model.ProcessExecutionChangeBearing {
		return a.codeChangeRationaleError(*jsonOut, "process_invalid", "validate active change-bearing PROCESS", errors.New("PROCESS is missing, superseded, or not change-bearing"))
	}
	if !gates.ReferencesArtifactID(process.Comment.Body, *specID) ||
		!linksContainURL(process.Comment.Links["Related Comments"], *specURL) {
		return a.codeChangeRationaleError(*jsonOut, "spec_link_missing", "validate PROCESS/SPEC linkage", fmt.Errorf("%s does not cover %s", *processID, *specID))
	}
	if !linksContainURL(process.Comment.Links["PR"], reference.CanonicalURL) {
		return a.codeChangeRationaleError(*jsonOut, "code_change_link_missing", "validate PROCESS code-change linkage", fmt.Errorf("%s does not link the active code change", *processID))
	}
	baseMarker := model.CodeChangeRationaleMarker{Process: *processID, Spec: *specID, SpecURL: *specURL,
		ProviderKey: reference.ProviderKey, ExternalRepository: reference.ExternalRepositoryID, ChangeID: reference.ExternalID,
		ReferenceVersion: reference.RepresentationVersion, SubjectRevision: revision, Agent: *agent}
	pendingMarker, err := model.PrepareCodeChangeRationaleMarker(baseMarker, body,
		model.CodeChangeRationalePendingExternal, "", "")
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "rationale_invalid", "prepare code-change rationale", err)
	}
	comments, err := issueBackend.ListIssueComments(ctx, repository, implement)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "comment_read_failed", "read existing rationale comments", err)
	}
	var exact []codeChangeRationaleCarrier
	for _, comment := range comments {
		if !model.IsLikelyCodeChangeRationale(comment.Body) {
			continue
		}
		existing, found, parseErr := model.FindCodeChangeRationaleMarker(comment.Body)
		if parseErr != nil {
			return a.codeChangeRationaleError(*jsonOut, "rationale_marker_invalid", "read existing rationale marker", parseErr)
		}
		if !found {
			continue
		}
		if model.CodeChangeRationaleVersion(existing) == 1 &&
			exactCodeChangeRationaleRetry(existing, baseMarker, comment.Body, body) {
			return a.outputCodeChangeRationale(codeChangeRationaleResultFor(repository, implement, comment,
				existing, false, true, "legacy_issue_only", ""), *jsonOut)
		}
		if existing.RationaleID == pendingMarker.RationaleID {
			exact = append(exact, codeChangeRationaleCarrier{Comment: comment, Marker: existing})
		}
	}
	if len(exact) > 1 {
		return a.codeChangeRationaleError(*jsonOut, "rationale_carrier_duplicate", "read existing rationale carrier",
			fmt.Errorf("rationale identity %s has %d carriers", pendingMarker.RationaleID, len(exact)))
	}
	var pending codeChangeRationaleCarrier
	if len(exact) == 1 {
		pending = exact[0]
		switch pending.Marker.Publication.State {
		case model.CodeChangeRationalePublishedExternal:
			return a.outputCodeChangeRationale(codeChangeRationaleResultFor(repository, implement, pending.Comment,
				pending.Marker, false, true, model.CodeChangeRationalePublishedExternal, "available"), *jsonOut)
		case model.CodeChangeRationaleExternalUnavailable:
			return a.outputCodeChangeRationale(codeChangeRationaleResultFor(repository, implement, pending.Comment,
				pending.Marker, false, true, model.CodeChangeRationaleExternalUnavailable, "unavailable"), *jsonOut)
		case model.CodeChangeRationalePendingExternal:
		default:
			return a.codeChangeRationaleError(*jsonOut, "rationale_state_invalid", "read existing rationale carrier",
				errors.New("unsupported publication state"))
		}
	}

	if err := validateCodeChangeRationaleProviderSelection(reference.ProviderKey); err != nil {
		return a.codeChangeRationaleError(*jsonOut, "provider_selection_invalid", "validate selected operator code provider", err)
	}
	provider, err := a.resolveOperatorProvider(ctx, profile, reference.ProviderKey)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "provider_unavailable", "resolve operator code provider", err)
	}
	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "provider_capability_failed", "read operator code provider capabilities", err)
	}
	if err := capabilities.Validate(); err != nil {
		return a.codeChangeRationaleError(*jsonOut, "provider_capability_invalid", "validate operator code provider capabilities", err)
	}
	if len(exact) == 0 {
		if err := requireExactCodeChangeRationaleTarget(ctx, backend, scope, issueID, reference, revision); err != nil {
			return a.codeChangeRationaleError(*jsonOut, "active_code_change_moved",
				"revalidate external code-change target before carrier creation", err)
		}
	}
	if !capabilities.Has(codereview.CapabilityChangeComment) {
		if len(exact) == 1 {
			return a.codeChangeRationaleError(*jsonOut, "rationale_state_conflict", "complete code-change rationale",
				errors.New("pending carrier exists but change.comment is no longer advertised"))
		}
		unavailableMarker, prepareErr := model.PrepareCodeChangeRationaleMarker(baseMarker, body,
			model.CodeChangeRationaleExternalUnavailable, "", "")
		if prepareErr != nil {
			return a.codeChangeRationaleError(*jsonOut, "rationale_invalid", "prepare issue-only code-change rationale", prepareErr)
		}
		rendered, renderErr := model.RenderCodeChangeRationaleBody(unavailableMarker, body)
		if renderErr != nil {
			return a.codeChangeRationaleError(*jsonOut, "rationale_invalid", "render issue-only code-change rationale", renderErr)
		}
		created, createErr := issueBackend.CreateComment(ctx, repository, implement, rendered)
		if createErr != nil {
			return a.codeChangeRationaleError(*jsonOut, "comment_create_failed", "create issue-only code-change rationale", createErr)
		}
		if created.ID <= 0 || created.Body != rendered {
			return a.codeChangeRationaleError(*jsonOut, "comment_create_invalid", "create issue-only code-change rationale",
				errors.New("response identity or body is incomplete or mismatched"))
		}
		observed, observeErr := observeExactCodeChangeRationaleCarrier(ctx, issueBackend, repository, implement,
			created.ID, rendered, rendered, unavailableMarker.RationaleID)
		if observeErr != nil {
			return a.codeChangeRationaleError(*jsonOut, "comment_create_unconfirmed",
				"confirm issue-only code-change rationale", observeErr)
		}
		return a.outputCodeChangeRationale(codeChangeRationaleResultFor(repository, implement, observed,
			unavailableMarker, true, false, model.CodeChangeRationaleExternalUnavailable, "unavailable"), *jsonOut)
	}
	mutationProvider, ok := provider.(codereview.MutationProvider)
	if !ok {
		return a.codeChangeRationaleError(*jsonOut, "provider_mutation_unavailable", "resolve operator code provider mutation",
			errors.New("change.comment is advertised but mutations are not implemented"))
	}

	pendingBody, err := model.RenderCodeChangeRationaleBody(pendingMarker, body)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "rationale_invalid", "render pending code-change rationale", err)
	}
	createdPending := false
	if len(exact) == 0 {
		created, createErr := issueBackend.CreateComment(ctx, repository, implement, pendingBody)
		if createErr != nil {
			return a.codeChangeRationaleError(*jsonOut, "comment_create_failed", "create pending code-change rationale", createErr)
		}
		if created.ID <= 0 || created.Body != pendingBody {
			return a.codeChangeRationaleError(*jsonOut, "comment_create_invalid", "create pending code-change rationale",
				errors.New("response identity or body is incomplete or mismatched"))
		}
		pending = codeChangeRationaleCarrier{Comment: created, Marker: pendingMarker}
		createdPending = true
	} else if pending.Comment.Body != pendingBody {
		return a.codeChangeRationaleError(*jsonOut, "rationale_state_conflict", "resume pending code-change rationale",
			errors.New("pending carrier body does not match the exact desired representation"))
	}

	if err := requireExactCodeChangeRationaleTarget(ctx, backend, scope, issueID, reference, revision); err != nil {
		return a.codeChangeRationaleError(*jsonOut, "active_code_change_moved", "revalidate external code-change target before publication", err)
	}
	projection, err := model.RenderCodeChangeRationaleExternalProjection(pendingMarker, body)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "rationale_projection_invalid", "render external rationale projection", err)
	}
	mutation, err := codereview.Mutate(ctx, mutationProvider, codereview.MutationRequest{
		Kind: codereview.MutationComment,
		Reference: codereview.Reference{ProviderKey: pendingMarker.ProviderKey,
			ExternalRepository: pendingMarker.ExternalRepository, ChangeID: pendingMarker.ChangeID},
		HeadRevision: pendingMarker.SubjectRevision,
		Body:         projection,
		Metadata: map[string]any{
			"kind": "rationale", "rationale_id": pendingMarker.RationaleID, "process": pendingMarker.Process,
			"spec": pendingMarker.Spec, "reference_version": pendingMarker.ReferenceVersion,
			"subject_revision": pendingMarker.SubjectRevision, "agent": pendingMarker.Agent,
		},
	})
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "provider_mutation_failed", "publish external code-change rationale", err)
	}
	publishedMarker, err := model.PrepareCodeChangeRationaleMarker(baseMarker, body,
		model.CodeChangeRationalePublishedExternal, mutation.ExternalID, mutation.CanonicalURL)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "provider_response_invalid", "validate external rationale receipt", err)
	}
	publishedBody, err := model.RenderCodeChangeRationaleBody(publishedMarker, body)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "rationale_invalid", "render published code-change rationale", err)
	}
	if err := requireExactCodeChangeRationaleTarget(ctx, backend, scope, issueID, reference, revision); err != nil {
		return a.codeChangeRationaleError(*jsonOut, "active_code_change_moved", "revalidate external code-change target after publication", err)
	}
	observed, err := observeExactCodeChangeRationaleCarrier(ctx, issueBackend, repository, implement,
		pending.Comment.ID, pendingBody, publishedBody, pendingMarker.RationaleID)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "rationale_reobserve_failed", "reobserve pending code-change rationale", err)
	}
	if observed.Body == publishedBody {
		return a.outputCodeChangeRationale(codeChangeRationaleResultFor(repository, implement, observed,
			publishedMarker, createdPending, true, model.CodeChangeRationalePublishedExternal, "available"), *jsonOut)
	}
	updated, err := issueBackend.UpdateComment(ctx, repository, pending.Comment.ID, publishedBody)
	if err != nil {
		return a.codeChangeRationaleError(*jsonOut, "comment_update_failed", "complete published code-change rationale", err)
	}
	if updated.ID != pending.Comment.ID || updated.Body != publishedBody {
		return a.codeChangeRationaleError(*jsonOut, "comment_update_invalid", "complete published code-change rationale",
			errors.New("response identity or body is incomplete or mismatched"))
	}
	completed, err := observeExactCodeChangeRationaleCarrier(ctx, issueBackend, repository, implement,
		pending.Comment.ID, publishedBody, publishedBody, pendingMarker.RationaleID)
	if err != nil || completed.Body != publishedBody {
		if err == nil {
			err = errors.New("completed carrier body was not observed")
		}
		return a.codeChangeRationaleError(*jsonOut, "comment_completion_unconfirmed", "confirm published code-change rationale", err)
	}
	return a.outputCodeChangeRationale(codeChangeRationaleResultFor(repository, implement, completed,
		publishedMarker, createdPending, false, model.CodeChangeRationalePublishedExternal, "available"), *jsonOut)
}

type codeChangeRationaleCarrier struct {
	Comment github.Comment
	Marker  model.CodeChangeRationaleMarker
}

func codeChangeRationaleResultFor(repository string, implement int, comment github.Comment,
	marker model.CodeChangeRationaleMarker, created, already bool, state, capability string) codeChangeRationaleResult {
	result := codeChangeRationaleResult{
		OK: true, Created: created, Already: already, Repo: repository, Implement: implement,
		CommentID: comment.ID, CommentURL: comment.HTMLURL, Process: marker.Process, Spec: marker.Spec,
		ProviderKey: marker.ProviderKey, ExternalRepository: marker.ExternalRepository, ChangeID: marker.ChangeID,
		SubjectRevision: marker.SubjectRevision, RepresentationVersion: marker.ReferenceVersion,
		PublicationState: state, RationaleID: marker.RationaleID, ExternalCapability: capability,
	}
	if marker.Publication != nil && marker.Publication.State == model.CodeChangeRationalePublishedExternal {
		result.ExternalCommentID = marker.Publication.ExternalID
		result.ExternalCommentURL = marker.Publication.ExternalURL
	}
	return result
}

func validateCodeChangeRationaleProviderSelection(providerKey string) error {
	plan, err := workflow.Resolve(".")
	if err != nil {
		return err
	}
	if plan.Config.ExternalCode != nil &&
		strings.TrimSpace(plan.Config.ExternalCode.ProviderKey) != strings.TrimSpace(providerKey) {
		return fmt.Errorf("workflow selects %s, active reference uses %s",
			plan.Config.ExternalCode.ProviderKey, providerKey)
	}
	return nil
}

func requireExactCodeChangeRationaleTarget(ctx context.Context, backend nativeCodeChangeBackend, scope models.RepoScope,
	issueID uuid.UUID, expected github.NativeReference, expectedRevision string) error {
	references, err := backend.ListNativeReferences(ctx, scope, issueID)
	if err != nil {
		return err
	}
	current, revision, err := uniqueActiveCodeChangeIdentity(references)
	if err != nil {
		return err
	}
	if current.ProviderKey != expected.ProviderKey ||
		current.ExternalRepositoryID != expected.ExternalRepositoryID ||
		current.ExternalID != expected.ExternalID ||
		current.RepresentationVersion != expected.RepresentationVersion ||
		revision != expectedRevision {
		return errors.New("active code-change provider, repository, change, reference version, or revision moved")
	}
	return nil
}

func observeExactCodeChangeRationaleCarrier(ctx context.Context, backend github.IssueBackend, repository string,
	implement int, commentID int64, expected, completed, rationaleID string) (github.Comment, error) {
	comments, err := backend.ListIssueComments(ctx, repository, implement)
	if err != nil {
		return github.Comment{}, err
	}
	matches := make([]github.Comment, 0, 1)
	identityCount := 0
	for _, comment := range comments {
		if model.IsLikelyCodeChangeRationale(comment.Body) {
			marker, found, parseErr := model.FindCodeChangeRationaleMarker(comment.Body)
			if parseErr != nil {
				return github.Comment{}, parseErr
			}
			if found && marker.RationaleID == rationaleID {
				identityCount++
			}
		}
		if comment.ID == commentID {
			matches = append(matches, comment)
		}
	}
	if identityCount != 1 {
		return github.Comment{}, fmt.Errorf("rationale identity has %d carriers", identityCount)
	}
	if len(matches) != 1 {
		return github.Comment{}, errors.New("rationale carrier is missing or duplicated")
	}
	if matches[0].Body != expected && matches[0].Body != completed {
		return github.Comment{}, errors.New("rationale carrier changed concurrently")
	}
	return matches[0], nil
}

func exactCodeChangeRationaleRetry(existing, desired model.CodeChangeRationaleMarker, existingBody, rationale string) bool {
	legacy := existing
	existing.AgentSessionID, existing.AgentSessionSource = "", ""
	desired.AgentSessionID, desired.AgentSessionSource = "", ""
	if existing != desired {
		return false
	}
	expected, err := model.RenderCodeChangeRationaleBody(legacy, rationale)
	if err != nil {
		return false
	}
	return stripLegacyRationaleSessionLines(existingBody) == stripLegacyRationaleSessionLines(expected)
}

func stripLegacyRationaleSessionLines(body string) string {
	lines := strings.Split(body, "\n")
	filtered := lines[:0]
	inRationale := false
	for _, line := range lines {
		if line == "### Rationale" {
			inRationale = true
		}
		if !inRationale && (strings.HasPrefix(line, "Agent Session ID:") || strings.HasPrefix(line, "Agent Session Source:")) {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

func (a *app) outputCodeChangeRationale(result codeChangeRationaleResult, jsonOut bool) int {
	if jsonOut {
		return a.outputJSON(result)
	}
	action := ""
	switch result.PublicationState {
	case model.CodeChangeRationalePublishedExternal:
		if result.Already {
			action = "already published externally"
		} else {
			action = "published externally"
		}
	case model.CodeChangeRationaleExternalUnavailable:
		if result.Already {
			action = "already recorded issue-only because change.comment is unavailable"
		} else {
			action = "recorded issue-only because change.comment is unavailable"
		}
	case "legacy_issue_only":
		action = "legacy issue-only rationale already exists"
	default:
		action = result.PublicationState
	}
	fmt.Fprintf(a.out, "%s for %s/%s at %s (reference version %d): %s\n",
		action, result.Process, result.Spec, result.SubjectRevision, result.RepresentationVersion, result.CommentURL)
	return 0
}

func (a *app) codeChangeRationaleError(jsonOut bool, code, operation string, err error) int {
	message := operation
	if err != nil {
		message += ": " + err.Error()
	}
	if jsonOut {
		_ = a.outputJSON(map[string]any{"ok": false, "code": code, "message": message})
	} else {
		a.errorf("%s\n", message)
	}
	return 1
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
