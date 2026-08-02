package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/relationships"
)

type linkTarget struct {
	Issue int    `json:"issue"`
	Type  string `json:"type"`
	ID    string `json:"id"`
}

type linkTargets struct {
	Version int          `json:"version"`
	Owner   linkTarget   `json:"owner"`
	Add     []linkTarget `json:"add,omitempty"`
	Remove  []linkTarget `json:"remove,omitempty"`
}

type linkResult struct {
	Version              int                             `json:"version"`
	OK                   bool                            `json:"ok"`
	Action               string                          `json:"action"`
	Kind                 relationships.Kind              `json:"kind,omitempty"`
	Owner                model.ArtifactRef               `json:"owner"`
	Add                  []model.ArtifactRef             `json:"add,omitempty"`
	Remove               []model.ArtifactRef             `json:"remove,omitempty"`
	LegacyPairNormalized bool                            `json:"legacy_pair_normalized"`
	ReverseWrites        int                             `json:"reverse_writes"`
	Atomic               bool                            `json:"atomic"`
	Guarantee            github.CommentMutationGuarantee `json:"guarantee"`
	RepresentationBefore int64                           `json:"representation_version_before,omitempty"`
	RepresentationAfter  int64                           `json:"representation_version_after,omitempty"`
	BeforeDigest         string                          `json:"before_digest"`
	AfterDigest          string                          `json:"after_digest"`
}

func (a *app) runLink(ctx context.Context, args []string) int {
	fs := newFlagSet("link", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	fromID := fs.String("from", "", "legacy pair endpoint typed comment id")
	fromIssueFlag := fs.String("from-issue", "", "legacy pair endpoint issue number or URL")
	toID := fs.String("to", "", "legacy pair endpoint typed comment id")
	toIssueFlag := fs.String("to-issue", "", "legacy pair endpoint issue number or URL")
	targetsPath := fs.String("targets-file", "", "versioned owner-oriented mutation JSON file, or - for stdin")
	allowNonAtomic := fs.Bool("allow-nonatomic", false, "allow one guarded non-conditional owner update")
	expectedDigest := fs.String("expected-digest", "", "exact expected owner body SHA-256 for non-atomic fallback")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}

	pairProvided := *fromID != "" || *toID != "" || *fromIssueFlag != "" || *toIssueFlag != ""
	fileProvided := strings.TrimSpace(*targetsPath) != ""
	if pairProvided == fileProvided {
		a.errorf("exactly one of the legacy --from/--to pair or --targets-file is required\n")
		return 2
	}
	var requested linkTargets
	legacyPair := false
	if fileProvided {
		var err error
		requested, err = readLinkTargets(*targetsPath, a.in)
		if err != nil {
			a.errorf("read link targets: %v\n", err)
			return 2
		}
	} else {
		if *fromID == "" || *toID == "" {
			a.errorf("--from and --to are both required for the legacy pair form\n")
			return 2
		}
		fromIssue, err := parseIssueFlag(*fromIssueFlag, "from-issue")
		if err != nil {
			a.errorf("%v\n", err)
			return 2
		}
		toIssue, err := parseIssueFlag(*toIssueFlag, "to-issue")
		if err != nil {
			a.errorf("%v\n", err)
			return 2
		}
		requested = linkTargets{Version: 1, Owner: linkTarget{Issue: fromIssue, ID: strings.TrimSpace(*fromID)},
			Add: []linkTarget{{Issue: toIssue, ID: strings.TrimSpace(*toID)}}}
		legacyPair = true
	}

	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for link on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	result, err := executeOwnerLink(ctx, client, repo, requested, legacyPair, *allowNonAtomic,
		strings.ToLower(strings.TrimSpace(*expectedDigest)))
	if err != nil {
		a.errorf("link: %v\n", err)
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	writeLinkResult(a.out, result)
	return 0
}

func writeLinkResult(out io.Writer, result linkResult) {
	if result.LegacyPairNormalized && result.Kind != "" && len(result.Add) == 1 && len(result.Remove) == 0 {
		orientation := fmt.Sprintf("%s -> %s", result.Owner.ID, result.Add[0].ID)
		if result.Action == "unchanged" {
			fmt.Fprintf(out, "relationship %s %s unchanged (reverse writes: 0)\n", result.Kind, orientation)
		} else {
			fmt.Fprintf(out, "updated relationship %s %s (reverse writes: 0, guarantee: %s)\n",
				result.Kind, orientation, result.Guarantee)
		}
		return
	}
	if result.Action == "unchanged" {
		fmt.Fprintf(out, "relationship owner %s unchanged (reverse writes: 0)\n", result.Owner.ID)
	} else {
		fmt.Fprintf(out, "updated relationship owner %s (reverse writes: 0, guarantee: %s)\n", result.Owner.ID, result.Guarantee)
	}
}

func readLinkTargets(path string, stdin io.Reader) (linkTargets, error) {
	var reader io.Reader
	var file *os.File
	if path == "-" {
		reader = stdin
	} else {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return linkTargets{}, err
		}
		defer file.Close()
		reader = file
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var targets linkTargets
	if err := decoder.Decode(&targets); err != nil {
		return targets, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return targets, errors.New("targets file contains trailing JSON")
	}
	if targets.Version != 1 || targets.Owner.Issue <= 0 || targets.Owner.ID == "" || len(targets.Add)+len(targets.Remove) == 0 {
		return targets, errors.New("targets file requires version 1, an owner, and at least one add or remove target")
	}
	if len(targets.Add)+len(targets.Remove) > relationships.DefaultMutationTargetLimit {
		return targets, fmt.Errorf("targets file exceeds the %d target limit", relationships.DefaultMutationTargetLimit)
	}
	return targets, nil
}

func executeOwnerLink(ctx context.Context, backend github.IssueBackend, repo string, requested linkTargets,
	legacyPair, allowNonAtomic bool, expectedDigest string) (linkResult, error) {
	locators := append([]linkTarget{requested.Owner}, requested.Add...)
	locators = append(locators, requested.Remove...)
	issues := map[int]bool{}
	for index := range locators {
		locators[index].ID = strings.TrimSpace(locators[index].ID)
		locators[index].Type = strings.ToUpper(strings.TrimSpace(locators[index].Type))
		if locators[index].Issue <= 0 || locators[index].ID == "" {
			return linkResult{}, fmt.Errorf("target %d has an invalid issue or id", index)
		}
		issues[locators[index].Issue] = true
	}
	issueNumbers := make([]int, 0, len(issues))
	for issue := range issues {
		issueNumbers = append(issueNumbers, issue)
	}
	sort.Ints(issueNumbers)
	comments := map[int][]github.Comment{}
	var artifacts []model.Artifact
	for _, issue := range issueNumbers {
		items, err := backend.ListIssueComments(ctx, repo, issue)
		if err != nil {
			return linkResult{}, fmt.Errorf("list issue %d comments: %w", issue, err)
		}
		comments[issue] = items
		for _, item := range items {
			typed := model.ParseTypedComment(item.Body)
			if typed.ID == "" {
				continue
			}
			artifacts = append(artifacts, model.Artifact{Issue: issue, CommentID: item.ID, URL: item.HTMLURL,
				APIURL: item.URL, Comment: typed})
		}
	}
	resolved := make([]model.Artifact, len(locators))
	for index, locator := range locators {
		var matches []model.Artifact
		for _, artifact := range artifacts {
			if artifact.Issue == locator.Issue && artifact.Comment.ID == locator.ID &&
				(locator.Type == "" || artifact.Comment.Type == locator.Type) {
				matches = append(matches, artifact)
			}
		}
		if len(matches) != 1 {
			return linkResult{}, fmt.Errorf("%s on issue %d has %d exact typed carriers", locator.ID, locator.Issue, len(matches))
		}
		resolved[index] = matches[0]
	}
	ownerRef, err := resolved[0].Ref()
	if err != nil {
		return linkResult{}, err
	}
	addRefs, removeRefs := make([]model.ArtifactRef, len(requested.Add)), make([]model.ArtifactRef, len(requested.Remove))
	for index := range addRefs {
		addRefs[index], err = resolved[index+1].Ref()
		if err != nil {
			return linkResult{}, err
		}
	}
	for index := range removeRefs {
		removeRefs[index], err = resolved[index+1+len(addRefs)].Ref()
		if err != nil {
			return linkResult{}, err
		}
	}
	var kind relationships.Kind
	if legacyPair {
		rule, normalizedOwner, normalizedTarget, err := relationships.Resolve(artifacts, ownerRef, addRefs[0])
		if err != nil {
			return linkResult{}, err
		}
		kind = rule.Kind
		ownerRef, addRefs[0] = normalizedOwner, normalizedTarget
	}

	ownerArtifactIndex := -1
	for index := range artifacts {
		ref, refErr := artifacts[index].Ref()
		if refErr == nil && ref.Key() == ownerRef.Key() && ref.URL == ownerRef.URL {
			ownerArtifactIndex = index
			break
		}
	}
	if ownerArtifactIndex < 0 {
		return linkResult{}, errors.New("resolved owner is absent from the frozen artifact snapshot")
	}

	conditional, conditionalErr := github.RequireConditionalCommentBackend(backend)
	var before github.Comment
	var beforeVersion int64
	strict := false
	if conditionalErr == nil {
		representation, observeErr := conditional.GetCommentRepresentation(ctx, repo, ownerRef.CommentID)
		if observeErr == nil {
			strict, before, beforeVersion = true, representation.Comment, representation.RepresentationVersion
		} else if !errors.Is(observeErr, github.ErrConditionalCommentMutationUnsupported) {
			return linkResult{}, fmt.Errorf("observe owner representation: %w", observeErr)
		}
	}
	if !strict {
		for _, item := range comments[ownerRef.Issue] {
			if item.ID == ownerRef.CommentID {
				before = item
				break
			}
		}
	}
	if before.ID != ownerRef.CommentID {
		return linkResult{}, errors.New("owner representation changed during resolution")
	}
	artifacts[ownerArtifactIndex].Comment = model.ParseTypedComment(before.Body)
	frozen, err := relationships.PlanMutation(artifacts, ownerRef, addRefs, removeRefs, before.Body, beforeVersion,
		relationships.DefaultMutationTargetLimit)
	if err != nil {
		return linkResult{}, err
	}
	result := linkResult{Version: relationships.MutationVersion, OK: true, Action: "unchanged", Kind: kind,
		Owner: ownerRef, Add: frozen.Mutation.Add,
		Remove: frozen.Mutation.Remove, LegacyPairNormalized: legacyPair, ReverseWrites: 0, Atomic: strict,
		BeforeDigest: frozen.BeforeDigest, AfterDigest: frozen.AfterDigest, RepresentationBefore: beforeVersion}
	if strict {
		result.Guarantee = github.CommentMutationStrictConditional
	} else {
		result.Guarantee = github.CommentMutationNonAtomicSingleWriter
	}
	if frozen.BeforeDigest == frozen.AfterDigest {
		result.RepresentationAfter = beforeVersion
		return result, nil
	}

	if strict {
		updated, updateErr := conditional.UpdateCommentConditional(ctx, repo, ownerRef.CommentID, beforeVersion, frozen.DesiredBody)
		observed, observeErr := conditional.GetCommentRepresentation(ctx, repo, ownerRef.CommentID)
		if observeErr != nil {
			if updateErr != nil {
				return linkResult{}, fmt.Errorf("conditional update outcome uncertain: %v; exact re-observation: %w", updateErr, observeErr)
			}
			return linkResult{}, fmt.Errorf("conditional update exact re-observation: %w", observeErr)
		}
		if observed.Comment.Body == frozen.DesiredBody {
			result.Action, result.RepresentationAfter = "updated", observed.RepresentationVersion
			return result, nil
		}
		if updateErr != nil {
			return linkResult{}, fmt.Errorf("conditional owner update failed: %w (current digest %s)", updateErr,
				model.RepresentationDigest(observed.Comment.Body))
		}
		return linkResult{}, fmt.Errorf("conditional owner update returned version %d but exact bytes differ (response version %d)",
			observed.RepresentationVersion, updated.RepresentationVersion)
	}

	if !allowNonAtomic {
		return linkResult{}, errors.New("conditional mutation unsupported; --allow-nonatomic and --expected-digest are required")
	}
	if expectedDigest == "" || expectedDigest != frozen.BeforeDigest {
		return linkResult{}, fmt.Errorf("--expected-digest must equal the observed owner digest %s", frozen.BeforeDigest)
	}
	fresh, err := observeLinkOwner(ctx, backend, repo, ownerRef)
	if err != nil {
		return linkResult{}, fmt.Errorf("pre-update exact observation: %w", err)
	}
	if model.RepresentationDigest(fresh.Body) != expectedDigest || fresh.Body != before.Body {
		return linkResult{}, fmt.Errorf("owner representation drifted before non-atomic update: expected=%s current=%s",
			expectedDigest, model.RepresentationDigest(fresh.Body))
	}
	_, updateErr := backend.UpdateComment(ctx, repo, ownerRef.CommentID, frozen.DesiredBody)
	confirmed, observeErr := observeLinkOwner(ctx, backend, repo, ownerRef)
	if observeErr != nil {
		if updateErr != nil {
			return linkResult{}, fmt.Errorf("non-atomic update outcome uncertain: %v; exact re-observation: %w", updateErr, observeErr)
		}
		return linkResult{}, fmt.Errorf("non-atomic update exact re-observation: %w", observeErr)
	}
	if confirmed.Body == frozen.DesiredBody {
		result.Action = "updated"
		return result, nil
	}
	if updateErr != nil {
		return linkResult{}, fmt.Errorf("non-atomic owner update failed: %w (current digest %s)", updateErr,
			model.RepresentationDigest(confirmed.Body))
	}
	return linkResult{}, fmt.Errorf("non-atomic owner update returned but exact bytes differ: expected=%s current=%s",
		frozen.AfterDigest, model.RepresentationDigest(confirmed.Body))
}

func observeLinkOwner(ctx context.Context, backend github.IssueBackend, repo string, owner model.ArtifactRef) (github.Comment, error) {
	items, err := backend.ListIssueComments(ctx, repo, owner.Issue)
	if err != nil {
		return github.Comment{}, err
	}
	var matches []github.Comment
	for _, item := range items {
		typed := model.ParseTypedComment(item.Body)
		if typed.Type == owner.Type && typed.ID == owner.ID && item.ID == owner.CommentID {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return github.Comment{}, fmt.Errorf("owner %s has %d exact provider carriers", owner.ID, len(matches))
	}
	return matches[0], nil
}
