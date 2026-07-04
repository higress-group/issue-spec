package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func (a *app) runComment(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec comment generate|upsert|list ...\n")
		return 2
	}
	switch args[0] {
	case "generate":
		return a.runCommentGenerate(ctx, args[1:])
	case "upsert":
		return a.runCommentUpsert(ctx, args[1:])
	case "list":
		return a.runCommentList(ctx, args[1:])
	default:
		a.errorf("unknown comment command %q\n", args[0])
		return 2
	}
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
	allowNoncanonical := fs.Bool("allow-noncanonical", false, "write-time migration bypass for noncanonical SPEC bodies; does not create durable approval")
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
	session := resolveWriterSession(*agentSession)
	body, err := model.EnsureTypedBody(*commentType, *id, rawBody, model.BodyOptions{Agent: *agent, AgentSessionID: session.ID, AgentSessionSource: session.Source, Status: *status, Scope: *scope})
	if err != nil {
		a.errorf("prepare typed comment body: %v\n", err)
		return 2
	}

	// Recompute canonical validity from the prepared body. SPEC is the only
	// strict blocking type today; other types return no diagnostics.
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
	action, comment, err := upsertTypedComment(ctx, client, repo, issueNumber, *commentType, *id, body)
	if err != nil {
		a.errorf("upsert comment: %v\n", err)
		return 1
	}
	result := map[string]any{"ok": true, "action": action, "issue": issueNumber, "comment_id": comment.ID, "url": comment.HTMLURL, "api_url": comment.URL, "type": strings.ToUpper(*commentType), "id": *id}
	if noncanonical {
		result["noncanonical"] = true
		result["canonical_diagnostics"] = diags
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s %s %s on issue #%d: %s\n", action, strings.ToUpper(*commentType), *id, issueNumber, comment.HTMLURL)
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
	var artifacts []model.Artifact
	for _, comment := range comments {
		if !model.IsLikelyTyped(comment.Body) {
			continue
		}
		tc := model.ParseTypedComment(comment.Body)
		if *commentType != "" && tc.Type != strings.ToUpper(*commentType) {
			continue
		}
		artifact := model.Artifact{Issue: issueNumber, CommentID: comment.ID, URL: comment.HTMLURL, APIURL: comment.URL, Comment: tc}
		artifact.Canonical = model.ValidateArtifact(artifact)
		artifacts = append(artifacts, artifact)
	}
	if *jsonOut {
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
