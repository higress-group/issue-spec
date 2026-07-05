package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/workflow"
)

type workflowTemplateData struct {
	Repo               string
	Change             string
	Kind               string
	Proposal           string
	Design             string
	Type               string
	ID                 string
	Agent              string
	Status             string
	Scope              string
	Input              map[string]any
	DefaultBody        string
	DefaultLogicalBody string
	Workflow           workflow.Plan
	Artifact           workflow.Artifact
}

func renderIssueBodyFromWorkflow(plan workflow.Plan, repo, kind, change, proposal, design, defaultBody string) (string, bool, error) {
	artifact, ok := plan.ArtifactForIssue(kind)
	if !ok {
		return defaultBody, false, nil
	}
	rendered, err := workflow.RenderTemplate(artifact.TemplatePath, workflowTemplateData{
		Repo:        repo,
		Change:      change,
		Kind:        kind,
		Proposal:    proposal,
		Design:      design,
		DefaultBody: defaultBody,
		Workflow:    plan,
		Artifact:    artifact,
	})
	if err != nil {
		return "", true, err
	}
	body, err := ensureIssueBodyMarker(kind, change, rendered)
	if err != nil {
		return "", true, err
	}
	return body, true, nil
}

func renderTypedBodyFromWorkflow(plan workflow.Plan, commentType, id, agent, status, scope, raw, defaultBody string) (string, bool, error) {
	artifact, ok := plan.ArtifactForComment(commentType)
	if !ok {
		return defaultBody, false, nil
	}
	input := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			return "", true, fmt.Errorf("parse input JSON for workflow template: %w", err)
		}
	}
	rendered, err := workflow.RenderTemplate(artifact.TemplatePath, workflowTemplateData{
		Type:               strings.ToUpper(strings.TrimSpace(commentType)),
		ID:                 id,
		Agent:              agent,
		Status:             status,
		Scope:              scope,
		Input:              input,
		DefaultBody:        defaultBody,
		DefaultLogicalBody: model.LogicalBody(defaultBody),
		Workflow:           plan,
		Artifact:           artifact,
	})
	if err != nil {
		return "", true, err
	}
	body, err := model.EnsureTypedBody(commentType, id, rendered, model.BodyOptions{Agent: agent, Status: status, Scope: scope})
	if err != nil {
		return "", true, err
	}
	if diags := model.ValidateCanonicalBody(commentType, id, "", body); len(diags) > 0 {
		return "", true, fmt.Errorf("%s %s is not canonical: %s", strings.ToUpper(strings.TrimSpace(commentType)), id, strings.Join(model.CanonicalDiagnosticStrings(diags), "; "))
	}
	return body, true, nil
}

func workflowNotice(plan workflow.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Project Workflow\n\n")
	fmt.Fprintf(&b, "- Workflow Source: `%s`\n", plan.Source.Kind)
	fmt.Fprintf(&b, "- Workflow Schema: `%s`\n", plan.Source.SchemaName)
	if plan.Source.ConfigPath != "" {
		fmt.Fprintf(&b, "- Workflow Config: `%s`\n", plan.Source.ConfigPath)
	}
	if plan.Source.TemplateDir != "" {
		fmt.Fprintf(&b, "- Workflow Template Directory: `%s`\n", plan.Source.TemplateDir)
	}
	if len(plan.Config.Context) > 0 {
		b.WriteString("- Workflow Context:\n")
		writeNoticeJSONBlock(&b, plan.Config.Context)
	}
	if len(plan.Config.Rules) > 0 {
		b.WriteString("- Workflow Rules:\n")
		writeNoticeJSONBlock(&b, plan.Config.Rules)
	}
	if hasArtifactInstructions(plan.Artifacts) {
		b.WriteString("- Artifact Instructions:\n")
		for _, artifact := range plan.Artifacts {
			instructions := strings.TrimSpace(artifact.Instructions)
			if instructions == "" {
				continue
			}
			fmt.Fprintf(&b, "  - `%s` (%s):\n", artifact.ID, artifact.Type)
			for _, line := range strings.Split(instructions, "\n") {
				fmt.Fprintf(&b, "    %s\n", strings.TrimRight(line, " \t"))
			}
		}
	}
	if len(plan.Diagnostics) > 0 {
		b.WriteString("- Workflow Diagnostics:\n")
		for _, diagnostic := range plan.Diagnostics {
			if diagnostic.Severity == "info" {
				continue
			}
			fmt.Fprintf(&b, "  - `%s/%s`: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
	b.WriteString("\nProject workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.\n")
	return b.String()
}

func writeNoticeJSONBlock(b *strings.Builder, value any) {
	raw, err := json.MarshalIndent(value, "  ", "  ")
	if err != nil {
		return
	}
	b.WriteString("  ```json\n")
	for _, line := range strings.Split(string(raw), "\n") {
		fmt.Fprintf(b, "  %s\n", line)
	}
	b.WriteString("  ```\n")
}

func hasArtifactInstructions(artifacts []workflow.Artifact) bool {
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Instructions) != "" {
			return true
		}
	}
	return false
}
