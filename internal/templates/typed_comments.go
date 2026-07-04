package templates

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

type QuestionOptions struct {
	ID                 string
	Agent              string
	AgentSessionID     string
	AgentSessionSource string
	Status             string
	Scope              string
	Blocking           bool
	Question           string
	Assumption         string
	Links              map[string][]string
}

func QuestionComment(opts QuestionOptions) (string, error) {
	if strings.TrimSpace(opts.Assumption) == "" {
		opts.Assumption = "N/A"
	}
	if strings.TrimSpace(opts.Status) == "" {
		if opts.Blocking {
			opts.Status = "blocked"
		} else {
			opts.Status = "draft"
		}
	}
	header := model.RenderHeader("QUESTION", opts.ID, model.BodyOptions{
		Agent:              opts.Agent,
		AgentSessionID:     opts.AgentSessionID,
		AgentSessionSource: opts.AgentSessionSource,
		Status:             opts.Status,
		Scope:              opts.Scope,
		Links:              opts.Links,
	})
	body := fmt.Sprintf(`%s
%s

## Question

%s

## Blocking

%t

## Default Assumption

%s

## Resolution Log

- Pending.
`, model.RenderMarker("QUESTION", opts.ID, 1), header, strings.TrimSpace(opts.Question), opts.Blocking, strings.TrimSpace(opts.Assumption))
	return model.EnsureTypedBody("QUESTION", opts.ID, body, model.BodyOptions{Agent: opts.Agent, AgentSessionID: opts.AgentSessionID, AgentSessionSource: opts.AgentSessionSource, Status: opts.Status, Scope: opts.Scope, Links: opts.Links})
}

// CommonOptions carries the shared typed-comment header fields for generated
// bodies across every typed comment family.
type CommonOptions struct {
	ID     string
	Agent  string
	Status string
	Scope  string
	Links  map[string][]string
}

func (c CommonOptions) bodyOptions() model.BodyOptions {
	return model.BodyOptions{Agent: c.Agent, Status: c.Status, Scope: c.Scope, Links: c.Links}
}

// SpecRequirementInput and SpecScenarioInput mirror the pinned SPEC generator
// JSON schema documented in the design issue.
type SpecRequirementInput struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type SpecScenarioInput struct {
	Title string `json:"title"`
	When  string `json:"when"`
	Then  string `json:"then"`
}

type SpecInput struct {
	Requirement SpecRequirementInput `json:"requirement"`
	Scenarios   []SpecScenarioInput  `json:"scenarios"`
}

type SpecCommentOptions struct {
	Common CommonOptions
	Input  SpecInput
}

// SpecComment renders a canonical SPEC typed comment body from structured input.
// The rendered body is guaranteed to pass the shared model SPEC validator so it
// can be piped directly into `comment upsert --type SPEC` without manual edits.
func SpecComment(opts SpecCommentOptions) (string, error) {
	title := strings.TrimSpace(opts.Input.Requirement.Title)
	text := strings.TrimSpace(opts.Input.Requirement.Text)
	if title == "" {
		return "", fmt.Errorf("requirement.title is required")
	}
	if text == "" {
		return "", fmt.Errorf("requirement.text is required")
	}
	if len(opts.Input.Scenarios) == 0 {
		return "", fmt.Errorf("at least one scenario is required")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Requirement: %s\n\n%s\n", title, text)
	for i, scenario := range opts.Input.Scenarios {
		scTitle := strings.TrimSpace(scenario.Title)
		when := strings.TrimSpace(scenario.When)
		then := strings.TrimSpace(scenario.Then)
		if scTitle == "" {
			return "", fmt.Errorf("scenarios[%d].title is required", i)
		}
		if when == "" {
			return "", fmt.Errorf("scenarios[%d].when is required", i)
		}
		if then == "" {
			return "", fmt.Errorf("scenarios[%d].then is required", i)
		}
		fmt.Fprintf(&b, "\n### Scenario: %s\n\n- **WHEN** %s\n- **THEN** %s\n", scTitle, when, then)
	}

	logical := b.String()
	if errs := model.SpecBodyErrors(logical); len(errs) > 0 {
		return "", fmt.Errorf("generated SPEC body is not canonical: %s", strings.Join(errs, "; "))
	}
	return model.EnsureTypedBody("SPEC", opts.Common.ID, logical, opts.Common.bodyOptions())
}

// TaskInput is the structured input for generated TASK bodies.
type TaskInput struct {
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Checklist []string `json:"checklist"`
	Covers    []string `json:"covers"`
}

type TaskCommentOptions struct {
	Common CommonOptions
	Input  TaskInput
}

func TaskComment(opts TaskCommentOptions) (string, error) {
	title := strings.TrimSpace(opts.Input.Title)
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Task: %s\n", title)
	if summary := strings.TrimSpace(opts.Input.Summary); summary != "" {
		fmt.Fprintf(&b, "\n%s\n", summary)
	}
	b.WriteString("\n### Implementation Checklist\n\n")
	writeChecklist(&b, opts.Input.Checklist)
	b.WriteString("\n### Covers\n\n")
	writeBulletRefs(&b, opts.Input.Covers)
	return model.EnsureTypedBody("TASK", opts.Common.ID, b.String(), opts.Common.bodyOptions())
}

// ProcessInput is the structured input for generated PROCESS bodies.
type ProcessInput struct {
	Title          string   `json:"title"`
	Owner          string   `json:"owner"`
	Scope          string   `json:"scope"`
	Dependencies   []string `json:"dependencies"`
	WriteOwnership []string `json:"write_ownership"`
	Covers         []string `json:"covers"`
	StatusNote     string   `json:"status_note"`
}

type ProcessCommentOptions struct {
	Common CommonOptions
	Input  ProcessInput
}

func ProcessComment(opts ProcessCommentOptions) (string, error) {
	title := strings.TrimSpace(opts.Input.Title)
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Process: %s\n", title)
	fmt.Fprintf(&b, "\n### Owner\n\n- %s\n", valueOr(strings.TrimSpace(opts.Input.Owner), "Worker Agent"))
	if scope := strings.TrimSpace(opts.Input.Scope); scope != "" {
		fmt.Fprintf(&b, "\n### Scope\n\n%s\n", scope)
	}
	b.WriteString("\n### Write Ownership\n\n")
	writeBulletRefs(&b, opts.Input.WriteOwnership)
	b.WriteString("\n### Dependencies\n\n")
	writeBulletRefs(&b, opts.Input.Dependencies)
	b.WriteString("\n### Covers\n\n")
	writeBulletRefs(&b, opts.Input.Covers)
	if note := strings.TrimSpace(opts.Input.StatusNote); note != "" {
		fmt.Fprintf(&b, "\n### Status\n\n%s\n", note)
	}
	return model.EnsureTypedBody("PROCESS", opts.Common.ID, b.String(), opts.Common.bodyOptions())
}

// ReviewInput is the structured input for generated (manual) REVIEW bodies.
// This template intentionally uses `## Review Summary` and never touches the
// established `## Review Sync Summary` shape produced by `review sync`.
type ReviewInput struct {
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Findings []string `json:"findings"`
	Verdict  string   `json:"verdict"`
}

type ReviewCommentOptions struct {
	Common CommonOptions
	Input  ReviewInput
}

func ReviewComment(opts ReviewCommentOptions) (string, error) {
	title := strings.TrimSpace(opts.Input.Title)
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Review Summary: %s\n", title)
	if summary := strings.TrimSpace(opts.Input.Summary); summary != "" {
		fmt.Fprintf(&b, "\n%s\n", summary)
	}
	b.WriteString("\n### Findings\n\n")
	writeBulletRefs(&b, opts.Input.Findings)
	fmt.Fprintf(&b, "\n### Verdict\n\n%s\n", valueOr(strings.TrimSpace(opts.Input.Verdict), "Pending."))
	return model.EnsureTypedBody("REVIEW", opts.Common.ID, b.String(), opts.Common.bodyOptions())
}

// VerifyInput is the structured input for generated VERIFY bodies.
type VerifyInput struct {
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
	SpecRefs []string `json:"spec_refs"`
}

type VerifyCommentOptions struct {
	Common CommonOptions
	Input  VerifyInput
}

func VerifyComment(opts VerifyCommentOptions) (string, error) {
	title := strings.TrimSpace(opts.Input.Title)
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Verification Summary: %s\n", title)
	if summary := strings.TrimSpace(opts.Input.Summary); summary != "" {
		fmt.Fprintf(&b, "\n%s\n", summary)
	}
	b.WriteString("\n### Evidence\n\n")
	writeBulletRefs(&b, opts.Input.Evidence)
	b.WriteString("\n### Covered SPECs\n\n")
	writeBulletRefs(&b, opts.Input.SpecRefs)
	return model.EnsureTypedBody("VERIFY", opts.Common.ID, b.String(), opts.Common.bodyOptions())
}

func writeChecklist(b *strings.Builder, items []string) {
	wrote := false
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Fprintf(b, "- [ ] %s\n", item)
		wrote = true
	}
	if !wrote {
		b.WriteString("- [ ] TBD\n")
	}
}

func writeBulletRefs(b *strings.Builder, items []string) {
	wrote := false
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Fprintf(b, "- %s\n", item)
		wrote = true
	}
	if !wrote {
		b.WriteString("- N/A\n")
	}
}
