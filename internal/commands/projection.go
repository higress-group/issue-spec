package commands

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/preview"
)

const projectionMarkerVersion = 1

var (
	projectionDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	projectionMarkerPattern = regexp.MustCompile(`(?m)^[ \t]*<!--[ \t]*issue-spec:projection[ \t]+([^>\r\n]*?)[ \t]*-->[ \t]*(?:\r?$)`)
	projectionPhases        = map[string]bool{
		"proposal-choice-brief":     true,
		"design-explainer":          true,
		"implement-execution-brief": true,
	}
)

type projectionMarker struct {
	Phase        string
	Owner        int
	Version      int
	SourceDigest string
}

type projectionMatch struct {
	Comment github.Comment
	Marker  projectionMarker
}

// conditionalProjectionCreateBackend is an optional backend capability whose
// implementation must atomically create at most one ordinary projection for
// the phase and owner identity. Backends that cannot enforce that uniqueness
// must not implement it; they use the explicit non-atomic fallback instead.
type conditionalProjectionCreateBackend interface {
	CreateProjectionCommentIfAbsent(context.Context, string, int, string, int, string) (github.CommentRepresentation, error)
}

type projectionUpsertResult struct {
	OK                    bool                            `json:"ok"`
	Action                string                          `json:"action"`
	Issue                 int                             `json:"issue"`
	CommentID             int64                           `json:"comment_id"`
	URL                   string                          `json:"url,omitempty"`
	Phase                 string                          `json:"phase"`
	Owner                 int                             `json:"owner"`
	SourceDigest          string                          `json:"source_digest"`
	MarkerVersion         int                             `json:"marker_version"`
	Guarantee             github.CommentMutationGuarantee `json:"guarantee,omitempty"`
	Atomic                bool                            `json:"atomic"`
	RepresentationVersion int64                           `json:"representation_version,omitempty"`
	BeforeDigest          string                          `json:"before_digest,omitempty"`
	AfterDigest           string                          `json:"after_digest"`
}

func (a *app) runProjection(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec projection upsert ...\n")
		return 2
	}
	switch args[0] {
	case "upsert":
		return a.runProjectionUpsert(ctx, args[1:])
	default:
		a.errorf("unknown projection command %q\n", args[0])
		return 2
	}
}

func (a *app) runProjectionUpsert(ctx context.Context, args []string) int {
	fs := newFlagSet("projection upsert", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	issueFlag := fs.String("issue", "", "phase issue number or URL; also becomes the projection owner")
	phaseFlag := fs.String("phase", "", "projection phase: proposal-choice-brief, design-explainer, or implement-execution-brief")
	sourceDigestFlag := fs.String("source-digest", "", "SHA-256 digest of the authoritative phase source")
	bodyFile := fs.String("body-file", "", "human-facing projection Markdown, or - for stdin; the command adds the projection marker")
	expectedDigestFlag := fs.String("expected-digest", "", "caller-observed SHA-256 digest of the existing projection body")
	allowNonAtomic := fs.Bool("allow-nonatomic", false, "explicitly permit a digest-guarded non-atomic GitHub-compatible update when CAS is unavailable")
	expectedAbsence := fs.Bool("expected-absence", false, "caller-observed that no projection exists; required with --allow-nonatomic before non-atomic creation")
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
	phase := strings.TrimSpace(*phaseFlag)
	if !projectionPhases[phase] {
		a.errorf("unsupported --phase %q; valid values: proposal-choice-brief, design-explainer, implement-execution-brief\n", phase)
		return 2
	}
	sourceDigest, err := parseProjectionDigest(*sourceDigestFlag, "source-digest", true)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	expectedDigest, err := parseProjectionDigest(*expectedDigestFlag, "expected-digest", false)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if *expectedAbsence && expectedDigest != "" {
		a.errorf("--expected-absence and --expected-digest are mutually exclusive\n")
		return 2
	}
	rawBody, ok := a.readBodyFile(*bodyFile)
	if !ok {
		return 2
	}
	body, err := prepareProjectionBody(rawBody, projectionMarker{
		Phase: phase, Owner: issue, Version: projectionMarkerVersion, SourceDigest: sourceDigest,
	})
	if err != nil {
		a.errorf("prepare projection body: %v\n", err)
		return 2
	}

	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for projection upsert on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	if err := validateIssueReferenceHost(*issueFlag, client.BackendInfo().Host); err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	comments, err := client.ListIssueComments(ctx, repo, issue)
	if err != nil {
		a.errorf("list issue comments: %v\n", err)
		return 1
	}
	match, found, err := findUniqueProjection(comments, phase, issue)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	if !found {
		if expectedDigest != "" {
			a.errorf("projection expected an existing body with digest %s, but no matching projection was found; no write performed\n", expectedDigest)
			return 1
		}
		return a.createProjection(ctx, client, repo, issue, body, phase, sourceDigest,
			*allowNonAtomic, *expectedAbsence, *jsonOut)
	}
	if *expectedAbsence {
		a.errorf("projection expected absence, but matching comment %d exists; no write performed\n", match.Comment.ID)
		return 1
	}

	conditional, capabilityErr := github.RequireConditionalCommentBackend(client)
	if capabilityErr == nil {
		observed, observeErr := conditional.GetCommentRepresentation(ctx, repo, match.Comment.ID)
		if observeErr == nil {
			return a.upsertProjectionStrict(ctx, conditional, repo, issue, match.Comment.ID, observed, body, phase, sourceDigest, expectedDigest, *jsonOut)
		}
		if !errors.Is(observeErr, github.ErrConditionalCommentMutationUnsupported) {
			a.errorf("observe projection: %v\n", observeErr)
			return 1
		}
		capabilityErr = observeErr
	}
	if !errors.Is(capabilityErr, github.ErrConditionalCommentMutationUnsupported) {
		a.errorf("conditional projection update: %v\n", capabilityErr)
		return 1
	}
	if match.Comment.Body == body {
		return a.outputProjection(projectionResult("unchanged", issue, match.Comment, phase, sourceDigest,
			github.CommentMutationNonAtomicSingleWriter, false, 0, match.Comment.Body, body), *jsonOut)
	}
	if !*allowNonAtomic || expectedDigest == "" {
		a.errorf("conditional projection update unsupported; no write performed (pass --allow-nonatomic and --expected-digest to acknowledge GitHub-compatible single-writer risk)\n")
		return 1
	}
	return a.upsertProjectionNonAtomic(ctx, client, repo, issue, match.Comment, body, phase, sourceDigest, expectedDigest, *jsonOut)
}

func (a *app) createProjection(ctx context.Context, client github.Backend, repo string, issue int, body, phase, sourceDigest string, allowNonAtomic, expectedAbsence, jsonOut bool) int {
	if conditional, ok := any(client).(conditionalProjectionCreateBackend); ok {
		created, err := conditional.CreateProjectionCommentIfAbsent(ctx, repo, issue, phase, issue, body)
		if err == nil {
			if created.Comment.Body != body {
				return a.nonAtomicPostWriteFailure("projection_post_write_mismatch", body, created.Comment.Body,
					"conditional projection create did not return the exact planned representation", jsonOut)
			}
			if err := validateObservedProjection(created.Comment, phase, issue); err != nil {
				a.errorf("conditional projection creation is invalid: %v\n", err)
				return 1
			}
			return a.outputProjection(projectionResult("created", issue, created.Comment, phase, sourceDigest,
				github.CommentMutationStrictConditional, true, created.RepresentationVersion, "", body), jsonOut)
		}
		if !errors.Is(err, github.ErrConditionalCommentMutationUnsupported) {
			a.errorf("conditional projection create: %v\n", err)
			return 1
		}
	}
	if !allowNonAtomic || !expectedAbsence {
		a.errorf("conditional projection create unsupported; no write performed (pass --allow-nonatomic and --expected-absence to acknowledge GitHub-compatible expected-absence single-writer risk)\n")
		return 1
	}
	created, err := client.CreateComment(ctx, repo, issue, body)
	if err != nil {
		a.errorf("create projection: %v\n", err)
		return 1
	}
	comments, err := client.ListIssueComments(ctx, repo, issue)
	if err != nil {
		return a.nonAtomicPostWriteFailure("projection_post_write_observation_failed", body, "", fmt.Sprintf("re-observe projection after create: %v", err), jsonOut)
	}
	match, found, err := findUniqueProjection(comments, phase, issue)
	if err != nil {
		return a.nonAtomicPostWriteFailure("projection_post_write_ambiguous", body, "",
			fmt.Sprintf("re-observe projection after create: %v", err), jsonOut)
	}
	if !found {
		return a.nonAtomicPostWriteFailure("projection_post_write_observation_failed", body, "",
			"re-observe projection after create: expected projection marker is absent", jsonOut)
	}
	if match.Comment.ID != created.ID {
		return a.nonAtomicPostWriteFailure("projection_post_write_identity_mismatch", body, match.Comment.Body,
			fmt.Sprintf("created projection id %d, observed matching id %d", created.ID, match.Comment.ID), jsonOut)
	}
	if match.Comment.Body != body {
		return a.nonAtomicPostWriteFailure("projection_post_write_mismatch", body, match.Comment.Body,
			"created projection did not persist exactly", jsonOut)
	}
	if match.Comment.HTMLURL == "" {
		match.Comment.HTMLURL = created.HTMLURL
	}
	return a.outputProjection(projectionResult("created", issue, match.Comment, phase, sourceDigest,
		github.CommentMutationNonAtomicSingleWriter, false, 0, "", body), jsonOut)
}

func (a *app) upsertProjectionStrict(ctx context.Context, backend github.ConditionalCommentBackend, repo string, issue int, commentID int64, observed github.CommentRepresentation, desired, phase, sourceDigest, expectedDigest string, jsonOut bool) int {
	if observed.Comment.ID != commentID {
		a.errorf("conditional projection observation changed comment identity\n")
		return 1
	}
	if err := validateObservedProjection(observed.Comment, phase, issue); err != nil {
		a.errorf("conditional projection observation is invalid: %v\n", err)
		return 1
	}
	if !digestMatches(observed.Comment.Body, expectedDigest) {
		a.errorf("projection body digest conflict: expected=%s current=%s\n", expectedDigest, bodyDigest(observed.Comment.Body))
		return 1
	}
	if observed.Comment.Body == desired {
		return a.outputProjection(projectionResult("unchanged", issue, observed.Comment, phase, sourceDigest,
			github.CommentMutationStrictConditional, true, observed.RepresentationVersion, observed.Comment.Body, desired), jsonOut)
	}
	updated, err := backend.UpdateCommentConditional(ctx, repo, commentID, observed.RepresentationVersion, desired)
	if err != nil {
		var conflict *github.CommentMutationConflictError
		if errors.As(err, &conflict) {
			return a.transitionConflict(conflict, jsonOut)
		}
		a.errorf("update projection: %v\n", err)
		return 1
	}
	if updated.Comment.ID != commentID || updated.Comment.Body != desired {
		return a.nonAtomicPostWriteFailure("projection_post_write_mismatch", desired, updated.Comment.Body, "conditional projection update did not return the exact planned representation", jsonOut)
	}
	if err := validateObservedProjection(updated.Comment, phase, issue); err != nil {
		a.errorf("updated projection observation is invalid: %v\n", err)
		return 1
	}
	return a.outputProjection(projectionResult("updated", issue, updated.Comment, phase, sourceDigest,
		github.CommentMutationStrictConditional, true, updated.RepresentationVersion, observed.Comment.Body, desired), jsonOut)
}

func (a *app) upsertProjectionNonAtomic(ctx context.Context, client github.Backend, repo string, issue int, current github.Comment, desired, phase, sourceDigest, expectedDigest string, jsonOut bool) int {
	if !digestMatches(current.Body, expectedDigest) {
		a.errorf("projection body digest conflict: expected=%s current=%s\n", expectedDigest, bodyDigest(current.Body))
		return 1
	}
	updated, err := client.UpdateComment(ctx, repo, current.ID, desired)
	if err != nil {
		a.errorf("update projection: %v\n", err)
		return 1
	}
	observed, err := observeProjectionComment(ctx, client, repo, issue, current.ID)
	if err != nil {
		return a.nonAtomicPostWriteFailure("projection_post_write_observation_failed", desired, "", fmt.Sprintf("re-observe projection after update: %v", err), jsonOut)
	}
	if observed.Body != desired {
		return a.nonAtomicPostWriteFailure("projection_post_write_mismatch", desired, observed.Body, "non-atomic projection update was overwritten or did not persist", jsonOut)
	}
	if err := validateObservedProjection(observed, phase, issue); err != nil {
		a.errorf("updated projection observation is invalid: %v\n", err)
		return 1
	}
	if observed.HTMLURL == "" {
		observed.HTMLURL = updated.HTMLURL
	}
	return a.outputProjection(projectionResult("updated", issue, observed, phase, sourceDigest,
		github.CommentMutationNonAtomicSingleWriter, false, 0, current.Body, desired), jsonOut)
}

func prepareProjectionBody(raw string, marker projectionMarker) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("--body-file must not be empty")
	}
	if matches := projectionMarkerPattern.FindAllStringSubmatch(preview.SemanticView(raw), -1); len(matches) > 0 {
		return "", errors.New("--body-file must not contain an issue-spec projection marker; the command owns the marker")
	}
	if _, found, err := model.FindMarker(raw); err != nil {
		return "", fmt.Errorf("typed marker is invalid: %w", err)
	} else if found {
		return "", errors.New("projection must remain an ordinary comment and cannot contain a typed marker")
	}
	content := strings.Trim(raw, "\r\n")
	return renderProjectionMarker(marker) + "\n\n" + content + "\n", nil
}

func renderProjectionMarker(marker projectionMarker) string {
	return fmt.Sprintf("<!-- issue-spec:projection phase=%s owner=%d version=%d source-digest=%s -->",
		marker.Phase, marker.Owner, marker.Version, marker.SourceDigest)
}

func parseProjectionDigest(raw, flagName string, required bool) (string, error) {
	value := normalizeDigest(raw)
	if value == "" && !required {
		return "", nil
	}
	if !projectionDigestPattern.MatchString(value) {
		return "", fmt.Errorf("--%s must be a SHA-256 digest", flagName)
	}
	return value, nil
}

func findUniqueProjection(comments []github.Comment, phase string, owner int) (projectionMatch, bool, error) {
	var matches []projectionMatch
	for _, comment := range comments {
		markers, err := parseProjectionMarkers(comment.Body)
		if err != nil {
			return projectionMatch{}, false, fmt.Errorf("comment %d has invalid projection marker: %w", comment.ID, err)
		}
		for _, marker := range markers {
			if marker.Phase == phase && marker.Owner == owner {
				matches = append(matches, projectionMatch{Comment: comment, Marker: marker})
			}
		}
	}
	if len(matches) == 0 {
		return projectionMatch{}, false, nil
	}
	if len(matches) != 1 {
		return projectionMatch{}, false, fmt.Errorf("projection phase %s owner %d is ambiguous: found %d matching markers", phase, owner, len(matches))
	}
	if _, found, err := model.FindMarker(matches[0].Comment.Body); err != nil {
		return projectionMatch{}, false, fmt.Errorf("projection comment %d has invalid typed syntax: %w", matches[0].Comment.ID, err)
	} else if found {
		return projectionMatch{}, false, fmt.Errorf("projection comment %d is typed; projections must remain ordinary comments", matches[0].Comment.ID)
	}
	return matches[0], true, nil
}

func parseProjectionMarkers(body string) ([]projectionMarker, error) {
	matches := projectionMarkerPattern.FindAllStringSubmatch(preview.SemanticView(body), -1)
	markers := make([]projectionMarker, 0, len(matches))
	for _, match := range matches {
		attrs := map[string]string{}
		for _, field := range strings.Fields(match[1]) {
			key, value, ok := strings.Cut(field, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			if !ok || key == "" || value == "" {
				return nil, fmt.Errorf("invalid attribute %q", field)
			}
			if _, duplicate := attrs[key]; duplicate {
				return nil, fmt.Errorf("duplicate attribute %q", key)
			}
			attrs[key] = value
		}
		for key := range attrs {
			switch key {
			case "phase", "owner", "version", "source-digest":
			default:
				return nil, fmt.Errorf("unsupported attribute %q", key)
			}
		}
		if len(attrs) != 4 {
			return nil, errors.New("projection marker requires phase, owner, version, and source-digest")
		}
		if !projectionPhases[attrs["phase"]] {
			return nil, fmt.Errorf("unsupported phase %q", attrs["phase"])
		}
		owner, err := strconv.Atoi(attrs["owner"])
		if err != nil || owner <= 0 {
			return nil, fmt.Errorf("invalid owner %q", attrs["owner"])
		}
		version, err := strconv.Atoi(attrs["version"])
		if err != nil || version != projectionMarkerVersion {
			return nil, fmt.Errorf("unsupported version %q", attrs["version"])
		}
		sourceDigest, err := parseProjectionDigest(attrs["source-digest"], "source-digest", true)
		if err != nil {
			return nil, err
		}
		markers = append(markers, projectionMarker{
			Phase: attrs["phase"], Owner: owner, Version: version, SourceDigest: sourceDigest,
		})
	}
	return markers, nil
}

func validateObservedProjection(comment github.Comment, phase string, owner int) error {
	match, found, err := findUniqueProjection([]github.Comment{comment}, phase, owner)
	if err != nil {
		return err
	}
	if !found || match.Comment.ID != comment.ID {
		return errors.New("expected projection marker is absent")
	}
	return nil
}

func observeProjectionComment(ctx context.Context, client github.Backend, repo string, issue int, commentID int64) (github.Comment, error) {
	if observer, ok := any(client).(github.IssueCommentObserver); ok {
		observed, err := observer.ObserveIssueComment(ctx, repo, commentID)
		if err != nil {
			return github.Comment{}, err
		}
		if observed.Comment.ID != commentID {
			return github.Comment{}, fmt.Errorf("expected comment id %d, observed %d", commentID, observed.Comment.ID)
		}
		return observed.Comment, nil
	}
	return observeCommentByID(ctx, client, repo, issue, commentID)
}

func projectionResult(action string, issue int, comment github.Comment, phase, sourceDigest string, guarantee github.CommentMutationGuarantee, atomic bool, version int64, before, after string) projectionUpsertResult {
	beforeDigest := ""
	if before != "" {
		beforeDigest = bodyDigest(before)
	}
	return projectionUpsertResult{
		OK: true, Action: action, Issue: issue, CommentID: comment.ID, URL: comment.HTMLURL,
		Phase: phase, Owner: issue, SourceDigest: sourceDigest, MarkerVersion: projectionMarkerVersion,
		Guarantee: guarantee, Atomic: atomic, RepresentationVersion: version,
		BeforeDigest: beforeDigest, AfterDigest: bodyDigest(after),
	}
}

func (a *app) outputProjection(result projectionUpsertResult, jsonOut bool) int {
	if jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s ordinary projection phase=%s owner=%d comment=%d guarantee=%s atomic=%v\n",
		result.Action, result.Phase, result.Owner, result.CommentID, result.Guarantee, result.Atomic)
	return 0
}
