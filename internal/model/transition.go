package model

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
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
	lines := strings.Split(body, "\n")
	heading := -1
	end := len(lines)
	for index, line := range lines {
		if strings.TrimSpace(line) == "### Handoff" {
			heading = index
			for next := index + 1; next < len(lines); next++ {
				trimmed := strings.TrimSpace(lines[next])
				if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
					end = next
					break
				}
			}
			break
		}
	}
	if heading < 0 {
		return "", errors.New("PROCESS is missing ### Handoff section")
	}
	existing := strings.TrimSpace(strings.Join(lines[heading+1:end], "\n"))
	desired := value
	if mutation.Append && !emptyTransitionSection(existing) {
		if containsExactParagraph(existing, value) {
			return body, nil
		}
		desired = existing + "\n\n" + value
	}
	replacement := []string{"", desired}
	updated := append([]string(nil), lines[:heading+1]...)
	updated = append(updated, replacement...)
	updated = append(updated, lines[end:]...)
	return strings.Join(updated, "\n"), nil
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
	if beforeLogical != afterLogical {
		return errors.New("transition changed undeclared logical body content")
	}
	beforeCanonical := canonicalElements(before.Type, before.ID, original)
	afterCanonical := canonicalElements(after.Type, after.ID, updated)
	if !reflect.DeepEqual(beforeCanonical, afterCanonical) {
		return errors.New("transition changed canonical validity")
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
	lines := strings.Split(logical, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "### Handoff" {
			continue
		}
		end := len(lines)
		for next := index + 1; next < len(lines); next++ {
			trimmed := strings.TrimSpace(lines[next])
			if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
				end = next
				break
			}
		}
		result := append([]string(nil), lines[:index+1]...)
		result = append(result, "", "<declared-handoff>")
		result = append(result, lines[end:]...)
		return strings.TrimSpace(strings.Join(result, "\n"))
	}
	return logical
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
