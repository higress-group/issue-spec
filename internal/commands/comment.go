package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
	"github.com/higress-group/issue-spec/internal/workflow"
)

// parseCoversSectionIDs extracts the reference IDs listed as bullets under the
// `### Covers` section of a generated TASK body. The section ends at the next
// `###` heading or end of body; `N/A` and blank bullets are ignored.
func parseCoversSectionIDs(body string) []string {
	var ids []string
	inSection := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			inSection = strings.EqualFold(trimmed, "### Covers")
			continue
		}
		if !inSection || !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		ref := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if ref == "" || strings.EqualFold(ref, "N/A") {
			continue
		}
		ids = append(ids, ref)
	}
	return ids
}

func (a *app) runComment(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec comment create|generate|upsert|transition|get|list ...\n")
		return 2
	}
	switch args[0] {
	case "create":
		return a.runCommentCreate(ctx, args[1:])
	case "generate":
		return a.runCommentGenerate(ctx, args[1:])
	case "upsert":
		return a.runCommentUpsert(ctx, args[1:])
	case "transition":
		return a.runCommentTransition(ctx, args[1:])
	case "get":
		return a.runCommentGet(ctx, args[1:])
	case "list":
		return a.runCommentList(ctx, args[1:])
	default:
		a.errorf("unknown comment command %q\n", args[0])
		return 2
	}
}

const defaultCommentLinkLimit = 10

type commentLinkReference struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	URL  string `json:"url"`
}

type commentLinkRelation struct {
	Count          int                    `json:"count"`
	Items          []commentLinkReference `json:"items"`
	TruncatedCount int                    `json:"truncated_count"`
}

// commentReadArtifact is deliberately separate from model.Artifact. Existing
// comment list callers retain the exact legacy JSON contract; only targeted or
// explicitly filtered reads opt into this bounded projection.
type commentReadArtifact struct {
	Issue                 int                            `json:"issue"`
	CommentID             int64                          `json:"comment_id"`
	URL                   string                         `json:"url"`
	APIURL                string                         `json:"api_url,omitempty"`
	Type                  string                         `json:"type"`
	ID                    string                         `json:"id"`
	Status                string                         `json:"status"`
	Scope                 string                         `json:"scope,omitempty"`
	SubjectRevision       string                         `json:"subject_revision,omitempty"`
	RepresentationVersion int64                          `json:"representation_version,omitempty"`
	RepresentationDigest  string                         `json:"representation_digest"`
	Links                 map[string]commentLinkRelation `json:"links"`
	Canonical             []model.CanonicalDiagnostic    `json:"canonical,omitempty"`
	Errors                []string                       `json:"errors,omitempty"`
	Body                  string                         `json:"body,omitempty"`
}

func (a *app) runCommentGet(ctx context.Context, args []string) int {
	fs := newFlagSet("comment get", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	id := fs.String("id", "", "stable typed comment id")
	commentType := fs.String("type", "", "expected typed comment type")
	commentID := fs.Int64("comment-id", 0, "provider comment id from a prior read")
	includeBody := fs.Bool("include-body", false, "include exact remote Markdown (requires --json)")
	includeAllLinks := fs.Bool("include-all-links", false, "return every link instead of at most 10 per relation (requires --json)")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if strings.TrimSpace(*id) == "" {
		a.errorf("--id is required\n")
		return 2
	}
	requestedType, ok := validateCommentReadType(*commentType)
	if !ok {
		a.errorf("unsupported --type %q\n", strings.ToUpper(strings.TrimSpace(*commentType)))
		return 2
	}
	if *commentID < 0 {
		a.errorf("--comment-id must be positive\n")
		return 2
	}
	if *includeBody && !*jsonOut {
		a.errorf("--include-body requires --json\n")
		return 2
	}
	if *includeAllLinks && !*jsonOut {
		a.errorf("--include-all-links requires --json\n")
		return 2
	}
	repo, valid := a.validateRepo(*repoFlag)
	if !valid {
		return 2
	}
	issueNumber, err := parseIssueFlag(*issueFlag, "issue")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for comment get on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}

	comment, observed, version, err := getTypedComment(ctx, client, repo, issueNumber, strings.TrimSpace(*id), requestedType, *commentID)
	if err != nil {
		a.errorf("get typed comment: %v\n", err)
		return 1
	}
	artifact := artifactFromComment(issueNumber, comment)
	index := linkIdentityIndex(observed)
	result := compactCommentArtifact(artifact, comment.Body, index, *includeBody, *includeAllLinks, version)
	if *jsonOut {
		return a.outputJSON(map[string]any{"ok": true, "issue": issueNumber, "comment": result})
	}
	fmt.Fprintf(a.out, "%s %s %s %s %s\n", result.Type, result.ID, result.Status, result.URL, result.RepresentationDigest)
	return 0
}

func (a *app) runCommentCreate(ctx context.Context, args []string) int {
	fs := newFlagSet("comment create", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	bodyFile := fs.String("body-file", "", "ordinary comment body file, or - for stdin")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	issueNumber, err := parseIssueFlag(*issueFlag, "issue")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	body, ok := a.readBodyFile(*bodyFile)
	if !ok {
		return 2
	}
	if strings.TrimSpace(body) == "" {
		a.errorf("--body-file must not be empty\n")
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for comment create on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	if err := validateIssueReferenceHost(*issueFlag, client.BackendInfo().Host); err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	comment, err := client.CreateComment(ctx, repo, issueNumber, body)
	if err != nil {
		a.errorf("create ordinary comment: %v\n", err)
		return 1
	}
	result := map[string]any{
		"ok":         true,
		"action":     "created",
		"issue":      issueNumber,
		"comment_id": comment.ID,
		"url":        comment.HTMLURL,
		"api_url":    comment.URL,
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "created ordinary comment %d on issue #%d: %s\n", comment.ID, issueNumber, comment.HTMLURL)
	return 0
}

func validateIssueReferenceHost(rawIssue, selectedHost string) error {
	rawIssue = strings.TrimSpace(rawIssue)
	if rawIssue == "" {
		return nil
	}
	u, err := url.Parse(rawIssue)
	if err != nil || u.Host == "" {
		return nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("--issue URL must use http or https")
	}
	want := hostnameOnly(selectedHost)
	got := strings.ToLower(u.Hostname())
	if want == "" || got != want {
		return fmt.Errorf("--issue URL host %q does not match selected issue backend host %q", got, want)
	}
	return nil
}

func hostnameOnly(raw string) string {
	raw = auth.NormalizeHost(raw)
	u, err := url.Parse("//" + raw)
	if err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	return strings.ToLower(raw)
}

func (a *app) runCommentGenerate(_ context.Context, args []string) int {
	fs := newFlagSet("comment generate", a.err)
	commentType := fs.String("type", "", "typed comment type")
	id := fs.String("id", "", "typed comment id")
	inputFile := fs.String("input-file", "", "structured input JSON file, or - for stdin")
	agent := fs.String("agent", "Coordinator", "logical agent identity")
	status := fs.String("status", "", "typed comment status")
	scope := fs.String("scope", "", "typed comment scope")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if strings.TrimSpace(*commentType) == "" {
		a.errorf("--type is required\n")
		return 2
	}
	if strings.TrimSpace(*id) == "" {
		a.errorf("--id is required\n")
		return 2
	}
	raw, ok := a.readFlagFile(*inputFile, "input-file")
	if !ok {
		return 2
	}
	body, err := generateTypedBody(*commentType, *id, *agent, *status, *scope, raw)
	if err != nil {
		a.errorf("generate typed comment: %v\n", err)
		return 2
	}
	workflowPlan, workflowErr := workflow.Resolve(".")
	if workflowErr != nil {
		a.errorf("workflow validation failed: %v\n", workflowErr)
		for _, diagnostic := range workflowPlan.Diagnostics {
			if diagnostic.Severity == "error" {
				a.errorf("- %s: %s\n", diagnostic.Code, diagnostic.Message)
			}
		}
		return 1
	}
	if rendered, used, err := renderTypedBodyFromWorkflow(workflowPlan, *commentType, *id, *agent, *status, *scope, raw, body); err != nil {
		a.errorf("render workflow typed comment template: %v\n", err)
		return 2
	} else if used {
		body = rendered
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	fmt.Fprint(a.out, body)
	return 0
}

func generateTypedBody(commentType, id, agent, status, scope, raw string) (string, error) {
	common := templates.CommonOptions{ID: id, Agent: agent, Status: status, Scope: scope}
	switch strings.ToUpper(strings.TrimSpace(commentType)) {
	case "SPEC":
		var input templates.SpecInput
		if err := decodeGeneratorInput(raw, &input); err != nil {
			return "", err
		}
		return templates.SpecComment(templates.SpecCommentOptions{Common: common, Input: input})
	case "TASK":
		var input templates.TaskInput
		if err := decodeGeneratorInput(raw, &input); err != nil {
			return "", err
		}
		return templates.TaskComment(templates.TaskCommentOptions{Common: common, Input: input})
	case "PROCESS":
		var input templates.ProcessInput
		if err := decodeGeneratorInput(raw, &input); err != nil {
			return "", err
		}
		return templates.ProcessComment(templates.ProcessCommentOptions{Common: common, Input: input})
	case "REVIEW":
		var input templates.ReviewInput
		if err := decodeGeneratorInput(raw, &input); err != nil {
			return "", err
		}
		return templates.ReviewComment(templates.ReviewCommentOptions{Common: common, Input: input})
	case "VERIFY":
		var input templates.VerifyInput
		if err := decodeGeneratorInput(raw, &input); err != nil {
			return "", err
		}
		return templates.VerifyComment(templates.VerifyCommentOptions{Common: common, Input: input})
	default:
		return "", fmt.Errorf("unsupported --type %q for comment generate; supported types: SPEC, TASK, PROCESS, REVIEW, VERIFY", commentType)
	}
}

func decodeGeneratorInput(raw string, target any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("parse input JSON: %w", err)
	}
	return nil
}

func (a *app) runCommentUpsert(ctx context.Context, args []string) int {
	fs := newFlagSet("comment upsert", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	commentType := fs.String("type", "", "typed comment type")
	id := fs.String("id", "", "typed comment id")
	bodyFile := fs.String("body-file", "", "markdown body file, or - for stdin")
	agent := fs.String("agent", "Coordinator", "logical agent identity")
	agentSession := addAgentSessionFlag(fs)
	status := fs.String("status", "draft", "typed comment status")
	scope := fs.String("scope", "N/A", "typed comment scope")
	allowNoncanonical := fs.Bool("allow-noncanonical", false, "write-time migration bypass for noncanonical SPEC/TASK/PROCESS bodies; does not create durable approval")
	coversIssue := fs.String("covers-issue", "", "for a TASK, the issue holding the SPEC comments named in ### Covers; resolves them to durable Related Comments links (forward + backlink)")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	issueNumber, err := parseIssueFlag(*issueFlag, "issue")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	rawBody, ok := a.readBodyFile(*bodyFile)
	if !ok {
		return 2
	}
	// Validate the --covers-issue flag combination before any auth/network so a
	// wrong --type fails fast instead of surfacing an auth error first.
	if strings.TrimSpace(*coversIssue) != "" && !strings.EqualFold(*commentType, "TASK") {
		a.errorf("--covers-issue only applies to --type TASK, got %q\n", strings.ToUpper(*commentType))
		return 2
	}
	session := resolveWriterSession(*agentSession)
	body, err := model.EnsureTypedBody(*commentType, *id, rawBody, model.BodyOptions{Agent: *agent, AgentSessionID: session.ID, AgentSessionSource: session.Source, Status: *status, Scope: *scope})
	if err != nil {
		a.errorf("prepare typed comment body: %v\n", err)
		return 2
	}

	// Recompute canonical validity from the prepared body. SPEC, TASK, and
	// PROCESS are strict blocking types; other types return no diagnostics.
	diags := model.ValidateCanonicalBody(*commentType, *id, "", body)
	noncanonical := false
	if len(diags) > 0 {
		if !*allowNoncanonical {
			a.errorf("%s %s is not canonical:\n", strings.ToUpper(*commentType), *id)
			for _, d := range diags {
				a.errorf("  - %s (%s)\n", d.Message, d.Element)
			}
			a.errorf("regenerate with `issue-spec comment generate --type %s`, or pass --allow-noncanonical for a write-time migration bypass. --allow-noncanonical does not create durable approval; status, verify, and archive keep reporting the noncanonical state.\n", strings.ToUpper(*commentType))
			return 2
		}
		noncanonical = true
	}

	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for comment upsert on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}

	// Durable covers (SPEC-002): for a TASK with --covers-issue, resolve the SPEC
	// IDs listed in ### Covers to peer comment URLs, splice them into this TASK's
	// Related Comments (forward link) before writing, and backlink each SPEC to the
	// TASK afterwards. A resolution failure is non-fatal so the upsert still lands.
	var coveredSpecs []model.Artifact
	var coveredSpecBodies []string
	if strings.TrimSpace(*coversIssue) != "" {
		coversIssueNumber, err := parseIssueFlag(*coversIssue, "covers-issue")
		if err != nil {
			a.errorf("%v\n", err)
			return 2
		}
		coversComments, err := client.ListIssueComments(ctx, repo, coversIssueNumber)
		if err != nil {
			a.errorf("list covers issue #%d: %v\n", coversIssueNumber, err)
			return 1
		}
		for _, specID := range parseCoversSectionIDs(body) {
			artifact, specBody, err := findArtifactByIDIn(coversComments, coversIssueNumber, specID)
			if err != nil {
				a.errorf("warning: covers %s not resolved on issue #%d: %v; skipping durable link\n", specID, coversIssueNumber, err)
				continue
			}
			merged, _, err := model.AddRelatedCommentLink(body, artifact.URL)
			if err != nil {
				a.errorf("warning: link covers %s: %v; skipping durable link\n", specID, err)
				continue
			}
			body = merged
			coveredSpecs = append(coveredSpecs, artifact)
			coveredSpecBodies = append(coveredSpecBodies, specBody)
		}
	}

	action, comment, dropped, err := upsertTypedComment(ctx, client, repo, issueNumber, *commentType, *id, body)
	if err != nil {
		a.errorf("upsert comment: %v\n", err)
		return 1
	}
	for i, spec := range coveredSpecs {
		newSpecBody, changed, err := model.AddRelatedCommentLink(coveredSpecBodies[i], comment.HTMLURL)
		if err != nil {
			a.errorf("warning: backlink %s -> %s: %v\n", spec.Comment.ID, *id, err)
			continue
		}
		if changed {
			if _, err := client.UpdateComment(ctx, repo, spec.CommentID, newSpecBody); err != nil {
				a.errorf("warning: patch backlink on %s: %v\n", spec.Comment.ID, err)
			}
		}
	}
	result := map[string]any{"ok": true, "action": action, "issue": issueNumber, "comment_id": comment.ID, "url": comment.HTMLURL, "api_url": comment.URL, "type": strings.ToUpper(*commentType), "id": *id}
	if noncanonical {
		result["noncanonical"] = true
		result["canonical_diagnostics"] = diags
	}
	if len(dropped) > 0 {
		result["dropped_related_comments"] = dropped
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s %s %s on issue #%d: %s\n", action, strings.ToUpper(*commentType), *id, issueNumber, comment.HTMLURL)
	if len(dropped) > 0 {
		fmt.Fprintf(a.out, "warning: %s %s on issue #%d dropped %d Related Comments link(s): %s\n", strings.ToUpper(*commentType), *id, issueNumber, len(dropped), strings.Join(dropped, ", "))
	}
	if noncanonical {
		fmt.Fprintf(a.out, "warning: wrote noncanonical %s %s with --allow-noncanonical; status, verify, and archive will keep reporting the noncanonical state until it is regenerated or superseded.\n", strings.ToUpper(*commentType), *id)
	}
	return 0
}

func (a *app) runCommentList(ctx context.Context, args []string) int {
	fs := newFlagSet("comment list", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	commentType := fs.String("type", "", "filter by typed comment type")
	status := fs.String("status", "", "filter by comma-separated typed comment statuses")
	activeOnly := fs.Bool("active-only", false, "return current non-superseded canonical artifacts")
	history := fs.Bool("history", false, "return superseded historical artifacts")
	jsonOut := fs.Bool("json", false, "write JSON output")
	includeBody := fs.Bool("include-body", false, "include original backend Markdown in JSON output (requires --json)")
	includeAllLinks := fs.Bool("include-all-links", false, "return every link instead of at most 10 per relation (requires --json)")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if *includeBody && !*jsonOut {
		a.errorf("--include-body requires --json\n")
		return 2
	}
	if *includeAllLinks && !*jsonOut {
		a.errorf("--include-all-links requires --json\n")
		return 2
	}
	if *activeOnly && *history {
		a.errorf("--active-only and --history are mutually exclusive\n")
		return 2
	}
	// Preserve the legacy list behavior for arbitrary type filters: an unknown
	// value simply produces no matches rather than becoming a new usage error.
	requestedType := strings.ToUpper(strings.TrimSpace(*commentType))
	statuses, err := parseCommentStatuses(*status)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	issueNumber, err := parseIssueFlag(*issueFlag, "issue")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for comment list on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	comments, err := client.ListIssueComments(ctx, repo, issueNumber)
	if err != nil {
		a.errorf("list issue comments: %v\n", err)
		return 1
	}
	artifacts := make([]model.Artifact, 0)
	artifactsWithBody := make([]commentListArtifactWithBody, 0)
	compactArtifacts := make([]commentReadArtifact, 0)
	advancedRead := *activeOnly || *history || len(statuses) > 0 || *includeAllLinks
	identityIndex := linkIdentityIndex(comments)
	for _, comment := range comments {
		if !model.IsLikelyTyped(comment.Body) {
			continue
		}
		tc := model.ParseTypedComment(comment.Body)
		if requestedType != "" && tc.Type != requestedType {
			continue
		}
		artifact := artifactFromComment(issueNumber, comment)
		if *activeOnly && (tc.Status == "superseded" || len(tc.Errors) > 0 || len(artifact.Canonical) > 0) {
			continue
		}
		if *history && tc.Status != "superseded" {
			continue
		}
		if len(statuses) > 0 && !statuses[tc.Status] {
			continue
		}
		artifacts = append(artifacts, artifact)
		if *includeBody {
			artifactsWithBody = append(artifactsWithBody, commentListArtifactWithBody{Artifact: artifact, Body: comment.Body})
		}
		if advancedRead {
			compactArtifacts = append(compactArtifacts, compactCommentArtifact(artifact, comment.Body, identityIndex, *includeBody, *includeAllLinks, 0))
		}
	}
	if *jsonOut {
		if advancedRead {
			return a.outputJSON(map[string]any{"ok": true, "issue": issueNumber, "comments": compactArtifacts})
		}
		if *includeBody {
			return a.outputJSON(map[string]any{"ok": true, "issue": issueNumber, "comments": artifactsWithBody})
		}
		return a.outputJSON(map[string]any{"ok": true, "issue": issueNumber, "comments": artifacts})
	}
	for _, artifact := range artifacts {
		tc := artifact.Comment
		fmt.Fprintf(a.out, "%-9s %-12s %-12s %-30s %s\n", tc.Type, tc.ID, tc.Status, tc.Scope, artifact.URL)
		if len(tc.Errors) > 0 {
			for _, parseErr := range tc.Errors {
				fmt.Fprintf(a.out, "  malformed: %s\n", parseErr)
			}
		}
		for _, d := range artifact.Canonical {
			fmt.Fprintf(a.out, "  noncanonical: %s (%s)\n", d.Message, d.Element)
		}
	}
	return 0
}

// commentListArtifactWithBody is deliberately command-local: model.Artifact
// and model.TypedComment retain their existing JSON contracts unless callers
// explicitly opt into the original backend Markdown for comment list.
type commentListArtifactWithBody struct {
	model.Artifact
	Body string `json:"body"`
}

func validateCommentReadType(raw string) (string, bool) {
	t := strings.ToUpper(strings.TrimSpace(raw))
	return t, t == "" || model.AllowedTypes[t]
}

func parseCommentStatuses(raw string) (map[string]bool, error) {
	statuses := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !model.AllowedStatuses[value] {
			return nil, fmt.Errorf("unsupported --status %q", value)
		}
		statuses[value] = true
	}
	return statuses, nil
}

func artifactFromComment(issue int, comment github.Comment) model.Artifact {
	artifact := model.Artifact{
		Issue: issue, CommentID: comment.ID, URL: comment.HTMLURL, APIURL: comment.URL,
		Comment: model.ParseTypedComment(comment.Body),
	}
	artifact.Canonical = model.ValidateArtifact(artifact)
	return artifact
}

func compactCommentArtifact(artifact model.Artifact, body string, identities map[string]commentLinkReference, includeBody, includeAllLinks bool, version int64) commentReadArtifact {
	tc := artifact.Comment
	result := commentReadArtifact{
		Issue: artifact.Issue, CommentID: artifact.CommentID, URL: artifact.URL, APIURL: artifact.APIURL,
		Type: tc.Type, ID: tc.ID, Status: tc.Status, Scope: tc.Scope, SubjectRevision: tc.SubjectRevision,
		RepresentationVersion: version, RepresentationDigest: model.RepresentationDigest(body),
		Links: boundedCommentLinks(tc, identities, includeAllLinks), Canonical: artifact.Canonical, Errors: tc.Errors,
	}
	if includeBody {
		result.Body = body
	}
	return result
}

func linkIdentityIndex(comments []github.Comment) map[string]commentLinkReference {
	index := map[string]commentLinkReference{}
	for _, comment := range comments {
		if !model.IsLikelyTyped(comment.Body) {
			continue
		}
		tc := model.ParseTypedComment(comment.Body)
		ref := commentLinkReference{Type: tc.Type, ID: tc.ID, URL: comment.HTMLURL}
		for _, value := range []string{comment.HTMLURL, comment.URL} {
			if normalized := model.NormalizeURL(value); normalized != "" {
				copy := ref
				copy.URL = value
				index[normalized] = copy
			}
		}
	}
	return index
}

func boundedCommentLinks(tc model.TypedComment, identities map[string]commentLinkReference, includeAll bool) map[string]commentLinkRelation {
	relations := map[string]commentLinkRelation{}
	for name, values := range tc.Links {
		seen := map[string]bool{}
		refs := make([]commentLinkReference, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			normalized := model.NormalizeURL(value)
			if value == "" || strings.EqualFold(value, "N/A") || seen[normalized] {
				continue
			}
			seen[normalized] = true
			ref, found := identities[normalized]
			if !found {
				ref = commentLinkReference{URL: value}
			} else if ref.URL == "" {
				ref.URL = value
			}
			refs = append(refs, ref)
		}
		if len(refs) == 0 {
			continue
		}
		sort.Slice(refs, func(i, j int) bool {
			left := refs[i].Type + "\x00" + refs[i].ID + "\x00" + refs[i].URL
			right := refs[j].Type + "\x00" + refs[j].ID + "\x00" + refs[j].URL
			return left < right
		})
		count := len(refs)
		if !includeAll && len(refs) > defaultCommentLinkLimit {
			refs = refs[:defaultCommentLinkLimit]
		}
		relations[name] = commentLinkRelation{Count: count, Items: refs, TruncatedCount: count - len(refs)}
	}
	return relations
}

func getTypedComment(ctx context.Context, client github.Backend, repo string, issue int, id, requestedType string, commentID int64) (github.Comment, []github.Comment, int64, error) {
	if commentID > 0 {
		if observer, ok := client.(github.IssueCommentObserver); ok {
			observed, err := observer.ObserveIssueComment(ctx, repo, commentID)
			if err != nil {
				return github.Comment{}, nil, 0, fmt.Errorf("observe provider comment %d: %w", commentID, err)
			}
			if observed.Comment.ID != commentID {
				return github.Comment{}, nil, 0, fmt.Errorf("provider locator %d returned comment %d", commentID, observed.Comment.ID)
			}
			if observedIssue, known := issueNumberFromComment(observed.Comment); known {
				if observedIssue != issue {
					return github.Comment{}, nil, 0, fmt.Errorf("provider comment %d belongs to issue #%d, not #%d", commentID, observedIssue, issue)
				}
				if err := validateTypedCommentTarget(observed.Comment.Body, id, requestedType, true); err != nil {
					return github.Comment{}, nil, 0, err
				}
				return observed.Comment, []github.Comment{observed.Comment}, observed.RepresentationVersion, nil
			}
			// A direct response without issue identity cannot prove the requested
			// issue binding. Fall through to the issue-scoped scan.
		}
	}

	comments, err := client.ListIssueComments(ctx, repo, issue)
	if err != nil {
		return github.Comment{}, nil, 0, fmt.Errorf("list issue comments: %w", err)
	}
	candidates := make([]github.Comment, 0, 1)
	for _, comment := range comments {
		if commentID > 0 && comment.ID != commentID {
			continue
		}
		if commentID > 0 {
			candidates = append(candidates, comment)
			continue
		}
		if !model.IsLikelyTyped(comment.Body) {
			continue
		}
		tc := model.ParseTypedComment(comment.Body)
		if tc.ID == id || tc.Marker.ID == id {
			candidates = append(candidates, comment)
		}
	}
	if len(candidates) == 0 {
		if commentID > 0 {
			return github.Comment{}, comments, 0, fmt.Errorf("provider comment %d was not found on issue #%d", commentID, issue)
		}
		return github.Comment{}, comments, 0, fmt.Errorf("typed comment %s was not found on issue #%d", id, issue)
	}
	if len(candidates) > 1 {
		return github.Comment{}, comments, 0, fmt.Errorf("duplicate typed comment id %s on issue #%d", id, issue)
	}
	if err := validateTypedCommentTarget(candidates[0].Body, id, requestedType, false); err != nil {
		return github.Comment{}, comments, 0, err
	}
	return candidates[0], comments, 0, nil
}

func validateTypedCommentTarget(body, id, requestedType string, requireMarker bool) error {
	if !model.IsLikelyTyped(body) {
		return fmt.Errorf("provider comment is not a typed artifact")
	}
	tc := model.ParseTypedComment(body)
	if requireMarker && (tc.Marker.ID == "" || tc.Marker.Type == "") {
		return fmt.Errorf("provider comment is missing its typed marker")
	}
	if len(tc.Errors) > 0 {
		return fmt.Errorf("typed marker/header mismatch: %s", strings.Join(tc.Errors, "; "))
	}
	if tc.ID != id || (tc.Marker.ID != "" && tc.Marker.ID != id) {
		return fmt.Errorf("provider comment id mismatch: requested %s, observed %s", id, tc.ID)
	}
	if requestedType != "" && (tc.Type != requestedType || (tc.Marker.Type != "" && tc.Marker.Type != requestedType)) {
		return fmt.Errorf("provider comment type mismatch: requested %s, observed %s", requestedType, tc.Type)
	}
	return nil
}

func issueNumberFromComment(comment github.Comment) (int, bool) {
	for _, raw := range []string{comment.IssueURL, comment.HTMLURL} {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] != "issues" {
				continue
			}
			n, err := strconv.Atoi(parts[i+1])
			if err == nil && n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}
