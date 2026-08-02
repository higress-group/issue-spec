package templates

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/durable"
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
	ChoiceModel        *model.ChoiceModel
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
	choiceSection := ""
	if opts.ChoiceModel != nil {
		if err := opts.ChoiceModel.Validate(); err != nil {
			return "", fmt.Errorf("choice model: %w", err)
		}
		payload, err := model.CanonicalJSON(opts.ChoiceModel)
		if err != nil {
			return "", fmt.Errorf("choice model: %w", err)
		}
		choiceSection = "\n## Choice Model\n\n```json\n" + payload + "\n```\n"
	}
	body := fmt.Sprintf(`%s
%s

## Question

%s

## Blocking

%t

## Default Assumption

%s
%s

## Resolution Log

- Pending.
`, model.RenderMarker("QUESTION", opts.ID, 1), header, strings.TrimSpace(opts.Question), opts.Blocking, strings.TrimSpace(opts.Assumption), strings.TrimRight(choiceSection, "\n"))
	return model.EnsureTypedBody("QUESTION", opts.ID, body, model.BodyOptions{Agent: opts.Agent, AgentSessionID: opts.AgentSessionID, AgentSessionSource: opts.AgentSessionSource, Status: opts.Status, Scope: opts.Scope, Links: opts.Links})
}

type AnswerOptions struct {
	ID      string
	Agent   string
	Scope   string
	Links   map[string][]string
	Payload model.AnswerPayload
}

// AnswerComment renders a creation-only typed ANSWER. It intentionally has no
// session metadata or transition fields: provider comment metadata supplies
// actor, creation time, edit state, and stable ordering authority.
func AnswerComment(opts AnswerOptions) (string, error) {
	if err := opts.Payload.Validate(); err != nil {
		return "", err
	}
	payload, err := model.CanonicalJSON(opts.Payload)
	if err != nil {
		return "", err
	}
	logical := answerSelectionSummary(opts.Payload.Selection) + "\n\n## Answer\n\n```json\n" + payload + "\n```\n"
	return model.EnsureTypedBody("ANSWER", opts.ID, logical, model.BodyOptions{
		Agent: opts.Agent, Status: "done", Scope: opts.Scope, Links: opts.Links,
	})
}

// answerSelectionSummary is the short human-readable line rendered before the
// canonical JSON. Readers (humans and agents) get the decision at a glance;
// the fenced JSON under ## Answer stays the only parsed authority.
func answerSelectionSummary(selection model.AnswerSelection) string {
	if strings.TrimSpace(selection.Custom) != "" {
		lines := strings.Split(strings.TrimRight(selection.Custom, "\n"), "\n")
		for i, line := range lines {
			lines[i] = "> " + line
		}
		return "Custom answer:\n\n" + strings.Join(lines, "\n")
	}
	labels := make([]string, 0, len(selection.Options))
	for _, option := range selection.Options {
		labels = append(labels, option.Label)
	}
	return "Selected: " + strings.Join(labels, ", ")
}

// CommonOptions carries the shared typed-comment header fields for generated
// bodies across every typed comment family.
type CommonOptions struct {
	ID              string
	Agent           string
	SubjectRevision string
	Status          string
	Scope           string
	Links           map[string][]string
}

func (c CommonOptions) bodyOptions() model.BodyOptions {
	return model.BodyOptions{Agent: c.Agent, SubjectRevision: c.SubjectRevision, Status: c.Status, Scope: c.Scope, Links: c.Links}
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
	Durable     *durable.Intent      `json:"durable,omitempty"`
}

type SpecCommentOptions struct {
	Common         CommonOptions
	Input          SpecInput
	RepositoryRoot string
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
	if opts.Input.Durable != nil {
		payload, err := durable.CanonicalJSON(*opts.Input.Durable, durable.ValidationOptions{
			RepositoryRoot:  opts.RepositoryRoot,
			SpecID:          strings.TrimSpace(opts.Common.ID),
			SpecRequirement: title,
		})
		if err != nil {
			return "", fmt.Errorf("durable intent: %w", err)
		}
		fmt.Fprintf(&b, "\n## Durable Intent\n\n```json\n%s\n```\n", payload)
	}

	logical := b.String()
	if errs := model.SpecBodyErrors(logical); len(errs) > 0 {
		return "", fmt.Errorf("generated SPEC body is not canonical: %s", strings.Join(errs, "; "))
	}
	return model.EnsureTypedBody("SPEC", opts.Common.ID, logical, opts.Common.bodyOptions())
}

// TaskExecutionPlanning is the structured PROCESS-planning metadata carried by a
// generated TASK body. It lets the coordinator decide serial-vs-parallel PROCESS
// decomposition from the TASK alone, so the `### Execution Planning` section is
// always rendered (with TBD/N/A defaults) and is required for canonical TASK
// discipline.
type TaskExecutionPlanning struct {
	OwnedAreas        []string `json:"owned_areas"`
	SharedTouchpoints []string `json:"shared_touchpoints"`
	Dependencies      []string `json:"dependencies"`
	Coupling          string   `json:"coupling"`
	ExecutionMode     string   `json:"execution_mode"`
	Complexity        string   `json:"complexity"`
}

// TaskInput is the structured input for generated TASK bodies.
type TaskInput struct {
	Title             string                `json:"title"`
	Summary           string                `json:"summary"`
	Checklist         []string              `json:"checklist"`
	Covers            []string              `json:"covers"`
	ExecutionPlanning TaskExecutionPlanning `json:"execution_planning"`
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
	writeExecutionPlanning(&b, opts.Input.ExecutionPlanning)
	b.WriteString("\n### Covers\n\n")
	writeBulletRefs(&b, opts.Input.Covers)
	return model.EnsureTypedBody("TASK", opts.Common.ID, b.String(), opts.Common.bodyOptions())
}

// writeExecutionPlanning renders the canonical `### Execution Planning` section.
// Labeled lines are always emitted so a coordinator can read coupling and
// execution mode even when a caller supplies only some fields.
func writeExecutionPlanning(b *strings.Builder, p TaskExecutionPlanning) {
	b.WriteString("\n### Execution Planning\n\n")
	b.WriteString("- Owned modules / write areas:\n")
	writeNestedBullets(b, p.OwnedAreas)
	b.WriteString("- Shared touchpoints:\n")
	writeNestedBullets(b, p.SharedTouchpoints)
	b.WriteString("- Dependency / interface assumptions:\n")
	writeNestedBullets(b, p.Dependencies)
	fmt.Fprintf(b, "- Coupling class: %s\n", valueOr(strings.TrimSpace(p.Coupling), "TBD"))
	fmt.Fprintf(b, "- Recommended execution mode: %s\n", valueOr(strings.TrimSpace(p.ExecutionMode), "TBD"))
	fmt.Fprintf(b, "- Complexity / split guidance: %s\n", valueOr(strings.TrimSpace(p.Complexity), "TBD"))
}

func writeNestedBullets(b *strings.Builder, items []string) {
	wrote := false
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Fprintf(b, "  - %s\n", item)
		wrote = true
	}
	if !wrote {
		b.WriteString("  - N/A\n")
	}
}

// ProcessInput is the structured input for generated PROCESS bodies. ParentTask
// is required for canonical PROCESS discipline (every PROCESS node belongs to
// exactly one parent TASK). Handoff carries the completion evidence a serial
// PROCESS node passes to the next node in its chain; it renders as N/A for
// parallel or not-yet-started nodes and is enforced for serial chains at verify.
type ProcessInput struct {
	Title               string                           `json:"title"`
	Owner               string                           `json:"owner"`
	ParentTask          string                           `json:"parent_task"`
	ExecutionClass      model.ProcessExecutionClass      `json:"execution_class"`
	WorkspaceManagement model.ProcessWorkspaceManagement `json:"workspace_management"`
	Scope               string                           `json:"scope"`
	Dependencies        []string                         `json:"dependencies"`
	WriteOwnership      []string                         `json:"write_ownership"`
	Workspace           *model.ProcessWorkspace          `json:"workspace,omitempty"`
	Assignment          *assignment.ProcessInput         `json:"assignment,omitempty"`
	Covers              []string                         `json:"covers"`
	Handoff             string                           `json:"handoff"`
	// StatusNote remains accepted as a legacy generator input, but new PROCESS
	// bodies use only the canonical typed-comment Status header.
	StatusNote string `json:"status_note"`
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
	executionClass := opts.Input.ExecutionClass
	if executionClass == "" {
		executionClass = model.ProcessExecutionChangeBearing
	}
	var err error
	if executionClass, err = model.ParseProcessExecutionClassValue(string(executionClass)); err != nil {
		return "", err
	}
	if executionClass == model.ProcessExecutionReview || executionClass == model.ProcessExecutionVerification {
		return "", fmt.Errorf("execution class %q was removed; use provider review and configured checks", executionClass)
	}
	workspaceManagement := opts.Input.WorkspaceManagement
	if workspaceManagement == "" {
		workspaceManagement = model.ProcessWorkspaceManaged
	}
	if workspaceManagement, err = model.ParseProcessWorkspaceManagementValue(string(workspaceManagement)); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Process: %s\n", title)
	fmt.Fprintf(&b, "\n### Owner\n\n- %s\n", valueOr(strings.TrimSpace(opts.Input.Owner), "Worker Agent"))
	fmt.Fprintf(&b, "\n### Parent TASK\n\n- %s\n", valueOr(strings.TrimSpace(opts.Input.ParentTask), "TBD"))
	fmt.Fprintf(&b, "\n### Execution Class\n\n- %s\n", executionClass)
	fmt.Fprintf(&b, "\n### Workspace Management\n\n- %s\n", workspaceManagement)
	if opts.Input.Workspace != nil {
		if string(opts.Input.Workspace.ExecutionClass) != string(executionClass) {
			return "", fmt.Errorf("workspace execution class %q does not match PROCESS execution class %q", opts.Input.Workspace.ExecutionClass, executionClass)
		}
		if strings.TrimSpace(opts.Input.Workspace.ProcessID) != strings.TrimSpace(opts.Common.ID) {
			return "", fmt.Errorf("workspace process id %q does not match PROCESS id %q", opts.Input.Workspace.ProcessID, opts.Common.ID)
		}
		workspace, err := model.RenderProcessWorkspaceSection(*opts.Input.Workspace)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n%s\n", workspace)
	}
	if opts.Input.Assignment != nil {
		payload, err := assignment.ProcessInputJSON(*opts.Input.Assignment)
		if err != nil {
			return "", fmt.Errorf("invalid PROCESS Assignment input: %w", err)
		}
		fmt.Fprintf(&b, "\n### Assignment\n\n```json\n%s\n```\n", payload)
	}
	if scope := strings.TrimSpace(opts.Input.Scope); scope != "" {
		fmt.Fprintf(&b, "\n### Scope\n\n%s\n", scope)
	}
	b.WriteString("\n### Write Ownership\n\n")
	writeBulletRefs(&b, opts.Input.WriteOwnership)
	b.WriteString("\n### Dependencies\n\n")
	writeBulletRefs(&b, opts.Input.Dependencies)
	b.WriteString("\n### Covers\n\n")
	writeBulletRefs(&b, opts.Input.Covers)
	fmt.Fprintf(&b, "\n### Handoff\n\n%s\n", valueOr(strings.TrimSpace(opts.Input.Handoff), "N/A"))
	return model.EnsureTypedBody("PROCESS", opts.Common.ID, b.String(), opts.Common.bodyOptions())
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
