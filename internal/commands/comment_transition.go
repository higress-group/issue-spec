package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

type commentTransitionResult struct {
	OK                    bool                            `json:"ok"`
	Action                string                          `json:"action"`
	Issue                 int                             `json:"issue"`
	CommentID             int64                           `json:"comment_id"`
	URL                   string                          `json:"url,omitempty"`
	Type                  string                          `json:"type"`
	ID                    string                          `json:"id"`
	FromStatus            string                          `json:"from_status"`
	ToStatus              string                          `json:"to_status"`
	Guarantee             github.CommentMutationGuarantee `json:"guarantee"`
	Atomic                bool                            `json:"atomic"`
	ExpectedVersion       int64                           `json:"expected_version,omitempty"`
	RepresentationVersion int64                           `json:"representation_version,omitempty"`
	BeforeDigest          string                          `json:"before_digest"`
	AfterDigest           string                          `json:"after_digest"`
}

func (a *app) runCommentTransition(ctx context.Context, args []string) int {
	fs := newFlagSet("comment transition", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	id := fs.String("id", "", "typed comment id")
	to := fs.String("to", "", "desired status")
	expectedVersion := fs.Int64("expected-version", 0, "caller-observed representation version")
	expectedDigest := fs.String("expected-digest", "", "caller-observed SHA-256 body digest")
	handoffFile := fs.String("handoff-file", "", "PROCESS handoff body file, or - for stdin")
	appendHandoff := fs.Bool("append-handoff", false, "append handoff idempotently instead of replacing it")
	prLink := fs.String("pr", "", "PR URL to add")
	relatedLink := fs.String("related", "", "related comment URL to add")
	allowNonAtomic := fs.Bool("allow-nonatomic", false, "explicitly permit a single-writer non-atomic update on backends without CAS")
	agentSession := addAgentSessionFlag(fs)
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	issue, err := parseIssueFlag(*issueFlag, "issue")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if strings.TrimSpace(*id) == "" || strings.TrimSpace(*to) == "" {
		a.errorf("--id and --to are required\n")
		return 2
	}
	if *expectedVersion < 0 {
		a.errorf("--expected-version must be positive\n")
		return 2
	}
	if *expectedVersion > 0 && strings.TrimSpace(*expectedDigest) != "" {
		a.errorf("--expected-version and --expected-digest are mutually exclusive\n")
		return 2
	}
	var handoff *model.HandoffMutation
	if *handoffFile != "" {
		value, ok := a.readFlagFile(*handoffFile, "handoff-file")
		if !ok {
			return 2
		}
		handoff = &model.HandoffMutation{Value: value, Append: *appendHandoff}
	} else if *appendHandoff {
		a.errorf("--append-handoff requires --handoff-file\n")
		return 2
	}

	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for comment transition on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	artifact, listedBody, err := findUniqueTransitionArtifactByID(ctx, client, repo, issue, *id)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	session := resolveWriterSession(*agentSession)
	request := model.TransitionRequest{ExpectedType: artifact.Comment.Type, ExpectedID: *id, ToStatus: *to,
		Handoff: handoff, AgentSessionID: session.ID, AgentSessionSource: session.Source}
	if strings.TrimSpace(*prLink) != "" {
		request.PRLinks = []string{*prLink}
	}
	if strings.TrimSpace(*relatedLink) != "" {
		request.RelatedLinks = []string{*relatedLink}
	}

	conditional, capabilityErr := github.RequireConditionalCommentBackend(client)
	if capabilityErr == nil {
		observed, err := conditional.GetCommentRepresentation(ctx, repo, artifact.CommentID)
		if err != nil {
			if errors.Is(err, github.ErrConditionalCommentMutationUnsupported) {
				capabilityErr = err
			} else {
				a.errorf("observe %s: %v\n", *id, err)
				return 1
			}
		} else {
			return a.applyStrictTransition(ctx, conditional, repo, issue, artifact, observed, request, *expectedVersion, *expectedDigest, *jsonOut)
		}
	}
	if !errors.Is(capabilityErr, github.ErrConditionalCommentMutationUnsupported) {
		a.errorf("conditional transition: %v\n", capabilityErr)
		return 1
	}
	if !*allowNonAtomic {
		a.errorf("conditional transition unsupported; no write performed (pass --allow-nonatomic to acknowledge single-writer risk)\n")
		return 1
	}
	if *expectedVersion > 0 {
		a.errorf("--expected-version requires conditional backend support; use --expected-digest with --allow-nonatomic\n")
		return 2
	}
	return a.applyNonAtomicTransition(ctx, client, repo, issue, artifact, listedBody, request, *expectedDigest, *jsonOut)
}

func findUniqueTransitionArtifactByID(ctx context.Context, client github.Operations, repo string, issueNumber int, id string) (model.Artifact, string, error) {
	comments, err := client.ListIssueComments(ctx, repo, issueNumber)
	if err != nil {
		return model.Artifact{}, "", err
	}
	var matches []github.Comment
	for _, comment := range comments {
		if model.ParseTypedComment(comment.Body).ID == id {
			matches = append(matches, comment)
		}
	}
	if len(matches) == 0 {
		return model.Artifact{}, "", fmt.Errorf("typed comment %s not found on issue %d", id, issueNumber)
	}
	if len(matches) != 1 {
		return model.Artifact{}, "", fmt.Errorf("typed comment %s is ambiguous on issue %d: found %d matching markers", id, issueNumber, len(matches))
	}
	comment := matches[0]
	return model.Artifact{
		Issue:     issueNumber,
		CommentID: comment.ID,
		URL:       comment.HTMLURL,
		APIURL:    comment.URL,
		Comment:   model.ParseTypedComment(comment.Body),
	}, comment.Body, nil
}

func (a *app) applyStrictTransition(ctx context.Context, backend github.ConditionalCommentBackend, repo string, issue int, artifact model.Artifact, observed github.CommentRepresentation, request model.TransitionRequest, expectedVersion int64, expectedDigest string, jsonOut bool) int {
	if expectedVersion > 0 && observed.RepresentationVersion != expectedVersion {
		return a.transitionConflict(&github.CommentMutationConflictError{Expected: expectedVersion, Current: observed.RepresentationVersion}, jsonOut)
	}
	if !digestMatches(observed.Comment.Body, expectedDigest) {
		a.errorf("comment body digest conflict: expected=%s current=%s\n", normalizeDigest(expectedDigest), bodyDigest(observed.Comment.Body))
		return 1
	}
	transition, err := model.ApplyTypedTransition(observed.Comment.Body, request)
	if err != nil {
		a.errorf("transition %s: %v\n", request.ExpectedID, err)
		return 2
	}
	result := transitionResult(issue, artifact, transition, github.CommentMutationStrictConditional, true, observed.RepresentationVersion, observed.RepresentationVersion, observed.Comment.Body)
	if !transition.Changed {
		return a.outputTransition(result, jsonOut)
	}
	updated, err := backend.UpdateCommentConditional(ctx, repo, artifact.CommentID, observed.RepresentationVersion, transition.Body)
	if err != nil {
		var conflict *github.CommentMutationConflictError
		if errors.As(err, &conflict) {
			return a.transitionConflict(conflict, jsonOut)
		}
		a.errorf("patch %s: %v\n", request.ExpectedID, err)
		return 1
	}
	result.Action, result.URL, result.RepresentationVersion = "updated", updated.Comment.HTMLURL, updated.RepresentationVersion
	return a.outputTransition(result, jsonOut)
}

func (a *app) applyNonAtomicTransition(ctx context.Context, client github.Operations, repo string, issue int, artifact model.Artifact, body string, request model.TransitionRequest, expectedDigest string, jsonOut bool) int {
	if !digestMatches(body, expectedDigest) {
		a.errorf("comment body digest conflict: expected=%s current=%s\n", normalizeDigest(expectedDigest), bodyDigest(body))
		return 1
	}
	transition, err := model.ApplyTypedTransition(body, request)
	if err != nil {
		a.errorf("transition %s: %v\n", request.ExpectedID, err)
		return 2
	}
	result := transitionResult(issue, artifact, transition, github.CommentMutationNonAtomicSingleWriter, false, 0, 0, body)
	if !transition.Changed {
		return a.outputTransition(result, jsonOut)
	}
	updated, err := client.UpdateComment(ctx, repo, artifact.CommentID, transition.Body)
	if err != nil {
		a.errorf("patch %s: %v\n", request.ExpectedID, err)
		return 1
	}
	observed, err := observeCommentByID(ctx, client, repo, issue, artifact.CommentID)
	if err != nil {
		return a.nonAtomicPostWriteFailure("comment_post_write_observation_failed", transition.Body, "", fmt.Sprintf("re-observe %s after patch: %v", request.ExpectedID, err), jsonOut)
	}
	if observed.Body != transition.Body {
		return a.nonAtomicPostWriteFailure("comment_post_write_mismatch", transition.Body, observed.Body, fmt.Sprintf("non-atomic patch %s was overwritten or did not persist", request.ExpectedID), jsonOut)
	}
	result.Action, result.URL, result.AfterDigest = "updated", observed.HTMLURL, bodyDigest(observed.Body)
	if result.URL == "" {
		result.URL = updated.HTMLURL
	}
	return a.outputTransition(result, jsonOut)
}

func observeCommentByID(ctx context.Context, client github.Operations, repo string, issue int, commentID int64) (github.Comment, error) {
	comments, err := client.ListIssueComments(ctx, repo, issue)
	if err != nil {
		return github.Comment{}, err
	}
	var matches []github.Comment
	for _, comment := range comments {
		if comment.ID == commentID {
			matches = append(matches, comment)
		}
	}
	if len(matches) != 1 {
		return github.Comment{}, fmt.Errorf("expected exactly one comment id %d, found %d", commentID, len(matches))
	}
	return matches[0], nil
}

func (a *app) nonAtomicPostWriteFailure(code, plannedBody, observedBody, message string, jsonOut bool) int {
	plannedDigest := bodyDigest(plannedBody)
	if jsonOut {
		payload := map[string]any{"ok": false, "code": code, "message": message, "planned_digest": plannedDigest}
		if observedBody != "" {
			payload["current_digest"] = bodyDigest(observedBody)
		}
		_ = a.outputJSON(payload)
		return 1
	}
	if observedBody == "" {
		a.errorf("%s; planned_digest=%s\n", message, plannedDigest)
	} else {
		a.errorf("%s; planned_digest=%s current_digest=%s\n", message, plannedDigest, bodyDigest(observedBody))
	}
	return 1
}

func transitionResult(issue int, artifact model.Artifact, transition model.TransitionResult, guarantee github.CommentMutationGuarantee, atomic bool, expected, current int64, before string) commentTransitionResult {
	action := "unchanged"
	if transition.Changed {
		action = "planned"
	}
	return commentTransitionResult{OK: true, Action: action, Issue: issue, CommentID: artifact.CommentID, URL: artifact.URL,
		Type: transition.Type, ID: transition.ID, FromStatus: transition.FromStatus, ToStatus: transition.ToStatus,
		Guarantee: guarantee, Atomic: atomic, ExpectedVersion: expected, RepresentationVersion: current,
		BeforeDigest: bodyDigest(before), AfterDigest: bodyDigest(transition.Body)}
}

func (a *app) transitionConflict(conflict *github.CommentMutationConflictError, jsonOut bool) int {
	if jsonOut {
		_ = a.outputJSON(map[string]any{"ok": false, "code": "comment_representation_conflict", "expected": conflict.Expected, "current": conflict.Current})
		return 1
	}
	a.errorf("comment representation conflict: expected=%d current=%d\n", conflict.Expected, conflict.Current)
	return 1
}

func (a *app) outputTransition(result commentTransitionResult, jsonOut bool) int {
	if jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s %s %s status %s -> %s guarantee=%s atomic=%v\n", result.Action, result.Type, result.ID, result.FromStatus, result.ToStatus, result.Guarantee, result.Atomic)
	return 0
}

func bodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
func normalizeDigest(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "sha256:"))
}
func digestMatches(body, expected string) bool {
	expected = normalizeDigest(expected)
	return expected == "" || expected == bodyDigest(body)
}
