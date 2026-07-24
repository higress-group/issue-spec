package model

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/processworkspace"
)

// HandoffMutation changes only the logical ### Handoff section of a PROCESS.
// Append is idempotent: a value already present is not appended again.
type HandoffMutation struct {
	Value  string
	Append bool
}

// TransitionRequest declares every structured field a caller may mutate. Empty
// optional fields are left byte-for-byte unchanged.
type TransitionRequest struct {
	ExpectedType       string
	ExpectedID         string
	ToStatus           string
	Handoff            *HandoffMutation
	Workspace          *ProcessWorkspace
	PRLinks            []string
	RelatedLinks       []string
	AgentSessionID     string
	AgentSessionSource string
}

type TransitionResult struct {
	Body       string
	Changed    bool
	Type       string
	ID         string
	FromStatus string
	ToStatus   string
}

// ApplyTypedTransition applies one type-specific status edge and optional
// declared structured mutations without regenerating the typed comment.
func ApplyTypedTransition(body string, request TransitionRequest) (TransitionResult, error) {
	before := ParseTypedComment(body)
	if !HasTypedMarker(body) || !before.HasHead {
		return TransitionResult{}, errors.New("transition requires a typed marker and visible header")
	}
	if !hasTransitionLinksHeader(body) {
		return TransitionResult{}, errors.New("transition requires a complete Links header block")
	}
	if len(before.Errors) > 0 {
		return TransitionResult{}, errors.New(strings.Join(before.Errors, "; "))
	}
	if before.Type == "ANSWER" {
		return TransitionResult{}, errors.New("ANSWER comments are immutable; append a new ANSWER instead")
	}
	if expected := strings.ToUpper(strings.TrimSpace(request.ExpectedType)); expected != "" && before.Type != expected {
		return TransitionResult{}, fmt.Errorf("typed comment type is %s, expected %s", before.Type, expected)
	}
	if expected := strings.TrimSpace(request.ExpectedID); expected != "" && before.ID != expected {
		return TransitionResult{}, fmt.Errorf("typed comment id is %s, expected %s", before.ID, expected)
	}
	to := strings.TrimSpace(request.ToStatus)
	if to == "" {
		to = before.Status
	}
	if !AllowedStatuses[to] {
		return TransitionResult{}, fmt.Errorf("unsupported status %s", to)
	}
	if before.Status != to && !allowedTransition(before.Type, before.Status, to) {
		return TransitionResult{}, fmt.Errorf("illegal %s transition %s -> %s", before.Type, before.Status, to)
	}
	if request.Handoff != nil && before.Type != "PROCESS" {
		return TransitionResult{}, fmt.Errorf("handoff mutation only applies to PROCESS, got %s", before.Type)
	}
	if request.Workspace != nil && before.Type != "PROCESS" {
		return TransitionResult{}, fmt.Errorf("workspace mutation only applies to PROCESS, got %s", before.Type)
	}
	if request.Workspace != nil {
		current := ParseProcessWorkspace(before.ID, "", body)
		if current.Explicit && current.Blocking() {
			return TransitionResult{}, errors.New(CanonicalDiagnosticStrings(current.Diagnostics)[0])
		}
		if current.Workspace != nil {
			if err := validateProcessWorkspaceMutation(*current.Workspace, *request.Workspace); err != nil {
				return TransitionResult{}, err
			}
		}
	}

	updated := body
	var err error
	if before.Status != to {
		updated, err = SetTypedCommentStatus(updated, to)
		if err != nil {
			return TransitionResult{}, err
		}
	}
	if request.Handoff != nil {
		updated, err = mutateHandoff(updated, *request.Handoff)
		if err != nil {
			return TransitionResult{}, err
		}
	}
	if request.Workspace != nil {
		updated, err = mutateWorkspace(updated, *request.Workspace)
		if err != nil {
			return TransitionResult{}, err
		}
	}
	for _, link := range request.PRLinks {
		updated, _, err = AddPRLink(updated, link)
		if err != nil {
			return TransitionResult{}, err
		}
	}
	for _, link := range request.RelatedLinks {
		updated, _, err = AddRelatedCommentLink(updated, link)
		if err != nil {
			return TransitionResult{}, err
		}
	}
	if strings.TrimSpace(request.AgentSessionID) != "" || strings.TrimSpace(request.AgentSessionSource) != "" {
		updated, err = StampTypedSessionMetadata(updated, request.AgentSessionID, request.AgentSessionSource)
		if err != nil {
			return TransitionResult{}, err
		}
	}

	after := ParseTypedComment(updated)
	if err := validateTransitionInvariants(before, after, body, updated, request); err != nil {
		return TransitionResult{}, err
	}
	return TransitionResult{Body: updated, Changed: updated != body, Type: before.Type, ID: before.ID,
		FromStatus: before.Status, ToStatus: after.Status}, nil
}

func allowedTransition(commentType, from, to string) bool {
	edges := transitionEdges[commentType]
	for _, candidate := range edges[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

var transitionEdges = map[string]map[string][]string{
	"SPEC": {
		"draft": {"blocked", "confirmed", "superseded"}, "blocked": {"draft", "confirmed", "superseded"},
		"confirmed": {"done", "superseded"}, "done": {"superseded"},
	},
	"TASK": {
		"draft": {"blocked", "confirmed", "ready", "superseded"}, "blocked": {"confirmed", "ready", "in-progress", "done", "superseded"},
		"confirmed": {"ready", "in-progress", "done", "superseded"}, "ready": {"in-progress", "done", "superseded"},
		"in-progress": {"blocked", "done", "superseded"}, "done": {"superseded"},
	},
	"PROCESS": {
		"draft": {"blocked", "confirmed", "ready", "in-progress", "superseded"}, "blocked": {"confirmed", "ready", "in-progress", "done", "superseded"},
		"confirmed": {"ready", "in-progress", "done", "superseded"}, "ready": {"in-progress", "done", "superseded"},
		"in-progress": {"blocked", "done", "superseded"}, "done": {"superseded"},
	},
	"QUESTION": {
		"draft": {"blocked", "confirmed", "done", "superseded"}, "blocked": {"confirmed", "done", "superseded"},
		"confirmed": {"done", "superseded"}, "done": {"superseded"},
	},
	"ANSWER": {},
	"REVIEW": {
		"draft": {"blocked", "confirmed", "ready", "in-progress", "done", "superseded"}, "blocked": {"confirmed", "ready", "in-progress", "done", "superseded"},
		"confirmed": {"ready", "in-progress", "done", "superseded"}, "ready": {"in-progress", "done", "superseded"},
		"in-progress": {"blocked", "done", "superseded"}, "done": {"superseded"},
	},
	"VERIFY": {
		"draft": {"blocked", "confirmed", "ready", "in-progress", "done", "superseded"}, "blocked": {"confirmed", "ready", "in-progress", "done", "superseded"},
		"confirmed": {"ready", "in-progress", "done", "superseded"}, "ready": {"in-progress", "done", "superseded"},
		"in-progress": {"blocked", "done", "superseded"}, "done": {"superseded"},
	},
}

func mutateHandoff(body string, mutation HandoffMutation) (string, error) {
	value := strings.TrimSpace(mutation.Value)
	if value == "" {
		return "", errors.New("handoff value is empty")
	}
	sections := markdownSectionRanges(body, "### Handoff")
	if len(sections) == 0 {
		return "", errors.New("PROCESS is missing ### Handoff section")
	}
	if len(sections) != 1 {
		return "", errors.New("PROCESS has multiple `### Handoff` sections")
	}
	section := sections[0]
	existing := strings.TrimSpace(body[section.ContentStart:section.End])
	desired := value
	if mutation.Append && !emptyTransitionSection(existing) {
		if containsExactParagraph(existing, value) {
			return body, nil
		}
		desired = existing + "\n\n" + value
	}
	replacement := "\n" + desired
	if section.End < len(body) {
		replacement += "\n\n"
	}
	return body[:section.ContentStart] + replacement + body[section.End:], nil
}

func mutateWorkspace(body string, workspace ProcessWorkspace) (string, error) {
	section, err := RenderProcessWorkspaceSection(workspace)
	if err != nil {
		return "", err
	}
	section += "\n\n"
	bounds := workspaceSectionBounds(body)
	if len(bounds) > 1 {
		return "", errors.New("PROCESS has multiple `### Workspace` sections")
	}
	if len(bounds) == 1 {
		return body[:bounds[0][0]] + section + body[bounds[0][1]:], nil
	}
	handoffs := markdownSectionRanges(body, "### Handoff")
	if len(handoffs) == 0 {
		return "", errors.New("PROCESS is missing ### Handoff section")
	}
	insert := handoffs[0].Start
	return body[:insert] + section + body[insert:], nil
}

// validateProcessWorkspaceMutation separates reservation identity from mutable
// lifecycle evidence. A structured transition may advance observations, but it
// must not silently rebind a PROCESS to another reservation or rewrite durable
// commit evidence.
func validateProcessWorkspaceMutation(before, after ProcessWorkspace) error {
	if before.SchemaVersion != after.SchemaVersion || before.WorkspaceID != after.WorkspaceID ||
		before.Repository != after.Repository || before.ProcessID != after.ProcessID ||
		before.ExecutionClass != after.ExecutionClass || before.Mode != after.Mode ||
		before.BaseSHA != after.BaseSHA || before.Branch != after.Branch ||
		before.DetachedRevision != after.DetachedRevision || before.IntegrationOwner != after.IntegrationOwner ||
		before.RuntimeNamespace != after.RuntimeNamespace || !before.CreatedAt.Equal(after.CreatedAt) ||
		!reflect.DeepEqual(before.WriteOwnership, after.WriteOwnership) ||
		!reflect.DeepEqual(before.SharedTouchpoints, after.SharedTouchpoints) ||
		!reflect.DeepEqual(before.RuntimeResources, after.RuntimeResources) {
		return errors.New("workspace mutation changed immutable reservation identity")
	}
	if before.State != after.State && !processworkspace.CanTransition(before.State, after.State, before.Mode) {
		return fmt.Errorf("illegal workspace lifecycle transition %s -> %s", before.State, after.State)
	}
	if before.ResultCommit != "" && before.ResultCommit != after.ResultCommit {
		return errors.New("workspace mutation cannot clear or replace result commit evidence")
	}
	if before.IntegrationSHA != "" && before.IntegrationSHA != after.IntegrationSHA {
		return errors.New("workspace mutation cannot clear or replace integration SHA evidence")
	}
	resultAdded := before.ResultCommit == "" && after.ResultCommit != ""
	integrationAdded := before.IntegrationSHA == "" && after.IntegrationSHA != ""
	if resultAdded && after.State != processworkspace.StateWorkerComplete {
		return errors.New("workspace result commit evidence may first appear only when entering worker-complete")
	}
	if integrationAdded && after.State != processworkspace.StateIntegrated {
		return errors.New("workspace integration SHA evidence may first appear only when entering integrated")
	}
	if before.State == after.State && (before.State == processworkspace.StateConflicted || before.State == processworkspace.StateCleanupPending || before.State == processworkspace.StateCleaned) &&
		(before.ResultCommit != after.ResultCommit || before.IntegrationSHA != after.IntegrationSHA) {
		return fmt.Errorf("workspace %s state cannot acquire or change commit evidence", before.State)
	}
	if !before.RetentionExpiresAt.IsZero() && (after.RetentionExpiresAt.IsZero() || after.RetentionExpiresAt.Before(before.RetentionExpiresAt)) {
		return errors.New("workspace retention expiration cannot be cleared or shortened")
	}
	if after.UpdatedAt.Before(before.UpdatedAt) {
		return errors.New("workspace updated_at cannot move backwards")
	}
	materialChange := before.State != after.State || before.ResultCommit != after.ResultCommit ||
		before.IntegrationSHA != after.IntegrationSHA || !before.RetentionExpiresAt.Equal(after.RetentionExpiresAt)
	if materialChange && !after.UpdatedAt.After(before.UpdatedAt) {
		return errors.New("workspace state, evidence, or retention changes require updated_at to advance")
	}
	return nil
}

func emptyTransitionSection(value string) bool {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "-"))
	return value == "" || strings.EqualFold(value, "N/A")
}

func containsExactParagraph(existing, value string) bool {
	for _, paragraph := range strings.Split(existing, "\n\n") {
		if strings.TrimSpace(paragraph) == value {
			return true
		}
	}
	return false
}

func validateTransitionInvariants(before, after TypedComment, original, updated string, request TransitionRequest) error {
	if len(after.Errors) > 0 {
		return errors.New(strings.Join(after.Errors, "; "))
	}
	if before.Marker != after.Marker || before.Type != after.Type || before.ID != after.ID || before.Agent != after.Agent || before.Scope != after.Scope {
		return errors.New("transition changed marker, identity, agent, or scope")
	}
	if strings.TrimSpace(request.AgentSessionID) == "" && before.AgentSessionID != after.AgentSessionID {
		return errors.New("transition changed agent session id without declaration")
	}
	if strings.TrimSpace(request.AgentSessionSource) == "" && before.AgentSessionSource != after.AgentSessionSource {
		return errors.New("transition changed agent session source without declaration")
	}
	beforeLinks := invariantLinks(before.Links, len(request.PRLinks) > 0, len(request.RelatedLinks) > 0)
	afterLinks := invariantLinks(after.Links, len(request.PRLinks) > 0, len(request.RelatedLinks) > 0)
	if !reflect.DeepEqual(beforeLinks, afterLinks) {
		return errors.New("transition changed unrelated links")
	}
	beforeLogical, afterLogical := transitionLogicalBody(original), transitionLogicalBody(updated)
	if request.Handoff != nil {
		beforeLogical = withoutHandoff(beforeLogical)
		afterLogical = withoutHandoff(afterLogical)
	}
	if request.Workspace != nil {
		beforeLogical = withoutWorkspace(beforeLogical)
		afterLogical = withoutWorkspace(afterLogical)
	}
	if beforeLogical != afterLogical {
		return errors.New("transition changed undeclared logical body content")
	}
	beforeCanonical := canonicalElements(before.Type, before.ID, original)
	afterCanonical := canonicalElements(after.Type, after.ID, updated)
	if !reflect.DeepEqual(beforeCanonical, afterCanonical) {
		return errors.New("transition changed canonical validity")
	}
	workspace := ParseProcessWorkspace(after.ID, "", updated)
	if workspace.Explicit && workspace.Blocking() {
		return errors.New(CanonicalDiagnosticStrings(workspace.Diagnostics)[0])
	}
	return nil
}

func invariantLinks(links map[string][]string, allowPR, allowRelated bool) map[string][]string {
	result := map[string][]string{}
	for key, values := range links {
		if (allowPR && key == "PR") || (allowRelated && key == "Related Comments") {
			continue
		}
		result[key] = append([]string(nil), values...)
		sort.Strings(result[key])
	}
	return result
}

func canonicalElements(commentType, id, body string) []string {
	diagnostics := ValidateCanonicalBody(commentType, id, "", body)
	result := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		result = append(result, diagnostic.Element)
	}
	sort.Strings(result)
	return result
}

func withoutHandoff(logical string) string {
	sections := markdownSectionRanges(logical, "### Handoff")
	if len(sections) != 1 {
		return logical
	}
	section := sections[0]
	replacement := "\n<declared-handoff>"
	if section.End < len(logical) {
		replacement += "\n\n"
	}
	return logical[:section.ContentStart] + replacement + logical[section.End:]
}

func withoutWorkspace(logical string) string {
	bounds := workspaceSectionBounds(logical)
	if len(bounds) != 1 {
		return logical
	}
	return logical[:bounds[0][0]] + logical[bounds[0][1]:]
}

// transitionLogicalBody strips the complete visible header, including session
// metadata that predates LogicalBody's fixed header-key list. Link invariants
// are checked separately, so declared header/link mutations cannot look like a
// logical-body change.
func transitionLogicalBody(body string) string {
	stripped := strings.TrimLeft(markerRe.ReplaceAllString(body, ""), "\n")
	lines := strings.Split(stripped, "\n")
	links := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "Links:" {
			links = index
			break
		}
	}
	if links < 0 {
		return strings.TrimSpace(stripped)
	}
	end := links + 1
	for end < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[end]), "- ") {
		end++
	}
	for end < len(lines) && strings.TrimSpace(lines[end]) == "" {
		end++
	}
	return strings.TrimSpace(strings.Join(lines[end:], "\n"))
}

func hasTransitionLinksHeader(body string) bool {
	for _, line := range strings.Split(markerRe.ReplaceAllString(body, ""), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Links:" {
			return true
		}
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			return false
		}
	}
	return false
}
