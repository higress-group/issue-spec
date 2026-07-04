package model

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var idRe = regexp.MustCompile(`^[A-Z]+-[0-9]{3,}$`)

var AllowedTypes = map[string]bool{
	"SPEC":     true,
	"TASK":     true,
	"PROCESS":  true,
	"QUESTION": true,
	"REVIEW":   true,
	"VERIFY":   true,
}

var AllowedStatuses = map[string]bool{
	"draft":       true,
	"blocked":     true,
	"confirmed":   true,
	"in-progress": true,
	"ready":       true,
	"done":        true,
	"superseded":  true,
}

type TypedComment struct {
	Marker             Marker              `json:"marker"`
	Agent              string              `json:"agent"`
	AgentSessionID     string              `json:"agent_session_id,omitempty"`
	AgentSessionSource string              `json:"agent_session_source,omitempty"`
	Type               string              `json:"type"`
	ID                 string              `json:"id"`
	Status             string              `json:"status"`
	Scope              string              `json:"scope"`
	Links              map[string][]string `json:"links"`
	Body               string              `json:"-"`
	Errors             []string            `json:"errors,omitempty"`
	HasHead            bool                `json:"has_header"`
}

type BodyOptions struct {
	Agent              string
	AgentSessionID     string
	AgentSessionSource string
	Status             string
	Scope              string
	Links              map[string][]string
}

func ParseTypedComment(body string) TypedComment {
	tc := TypedComment{Links: map[string][]string{}, Body: body}
	marker, hasMarker, err := FindMarker(body)
	if err != nil {
		tc.Errors = append(tc.Errors, err.Error())
	}
	if hasMarker {
		tc.Marker = marker
		tc.Type = marker.Type
		tc.ID = marker.ID
	}

	lines := strings.Split(body, "\n")
	inLinks := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<!--") || trimmed == "" {
			if inLinks && trimmed == "" {
				break
			}
			continue
		}
		if inLinks {
			if strings.HasPrefix(trimmed, "- ") {
				name, value, ok := parseLinkLine(trimmed)
				if ok {
					tc.Links[name] = splitLinkValues(value)
					continue
				}
			}
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			if tc.HasHead {
				break
			}
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "Agent":
			tc.Agent = value
			tc.HasHead = true
		case "Agent Session ID":
			tc.AgentSessionID = value
			tc.HasHead = true
		case "Agent Session Source":
			tc.AgentSessionSource = value
			tc.HasHead = true
		case "Type":
			if tc.Type != "" && tc.Type != strings.ToUpper(value) {
				tc.Errors = append(tc.Errors, fmt.Sprintf("marker type %s does not match header type %s", tc.Type, value))
			}
			tc.Type = strings.ToUpper(value)
			tc.HasHead = true
		case "ID":
			if tc.ID != "" && tc.ID != value {
				tc.Errors = append(tc.Errors, fmt.Sprintf("marker id %s does not match header id %s", tc.ID, value))
			}
			tc.ID = value
			tc.HasHead = true
		case "Status":
			tc.Status = value
			tc.HasHead = true
		case "Scope":
			tc.Scope = value
			tc.HasHead = true
		case "Links":
			inLinks = true
			tc.HasHead = true
		default:
			if tc.HasHead {
				break
			}
		}
	}

	if tc.Type != "" && !AllowedTypes[tc.Type] {
		tc.Errors = append(tc.Errors, fmt.Sprintf("unsupported type %s", tc.Type))
	}
	if tc.ID != "" && !idRe.MatchString(tc.ID) {
		tc.Errors = append(tc.Errors, fmt.Sprintf("invalid id %s", tc.ID))
	}
	if tc.Status != "" && !AllowedStatuses[tc.Status] {
		tc.Errors = append(tc.Errors, fmt.Sprintf("unsupported status %s", tc.Status))
	}
	if hasMarker && !tc.HasHead {
		tc.Errors = append(tc.Errors, "typed comment is missing visible header")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"Agent", tc.Agent},
		{"Type", tc.Type},
		{"ID", tc.ID},
		{"Status", tc.Status},
		{"Scope", tc.Scope},
	} {
		if hasMarker && strings.TrimSpace(field.value) == "" {
			tc.Errors = append(tc.Errors, "typed comment is missing "+field.name)
		}
	}
	return tc
}

func EnsureTypedBody(commentType, id, body string, opts BodyOptions) (string, error) {
	commentType = strings.ToUpper(strings.TrimSpace(commentType))
	id = strings.TrimSpace(id)
	if !AllowedTypes[commentType] {
		return "", fmt.Errorf("unsupported type %s", commentType)
	}
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("invalid id %s", id)
	}
	if opts.Agent == "" {
		opts.Agent = "Coordinator"
	}
	if opts.Status == "" {
		opts.Status = "draft"
	}
	if opts.Scope == "" {
		opts.Scope = "N/A"
	}
	if !AllowedStatuses[opts.Status] {
		return "", fmt.Errorf("unsupported status %s", opts.Status)
	}

	tc := ParseTypedComment(body)
	if tc.Marker.Type != "" && (tc.Marker.Type != commentType || tc.Marker.ID != id) {
		return "", fmt.Errorf("body marker is %s/%s, command requested %s/%s", tc.Marker.Type, tc.Marker.ID, commentType, id)
	}
	if tc.HasHead {
		if tc.Type != "" && tc.Type != commentType {
			return "", fmt.Errorf("body header type is %s, command requested %s", tc.Type, commentType)
		}
		if tc.ID != "" && tc.ID != id {
			return "", fmt.Errorf("body header id is %s, command requested %s", tc.ID, id)
		}
		if len(tc.Errors) > 0 {
			return "", errors.New(strings.Join(tc.Errors, "; "))
		}
		if !HasTypedMarker(body) {
			body = RenderMarker(commentType, id, 1) + "\n" + strings.TrimLeft(body, "\n")
		}
		if opts.AgentSessionID != "" || opts.AgentSessionSource != "" {
			return StampTypedSessionMetadata(body, opts.AgentSessionID, opts.AgentSessionSource)
		}
		return body, nil
	}

	content := strings.TrimSpace(body)
	if content == "" {
		content = "## Summary\n\nTBD"
	}
	return RenderMarker(commentType, id, 1) + "\n" + RenderHeader(commentType, id, opts) + "\n\n" + content + "\n", nil
}

func RenderHeader(commentType, id string, opts BodyOptions) string {
	links := defaultLinks(opts.Links)
	keys := []string{"Proposal Issue", "Design Issue", "Implement Issue", "Related Comments", "PR"}
	var b strings.Builder
	fmt.Fprintf(&b, "Agent: %s\n", valueOr(opts.Agent, "Coordinator"))
	if strings.TrimSpace(opts.AgentSessionID) != "" {
		fmt.Fprintf(&b, "Agent Session ID: %s\n", strings.TrimSpace(opts.AgentSessionID))
	}
	if strings.TrimSpace(opts.AgentSessionSource) != "" {
		fmt.Fprintf(&b, "Agent Session Source: %s\n", strings.TrimSpace(opts.AgentSessionSource))
	}
	fmt.Fprintf(&b, "Type: %s\n", strings.ToUpper(commentType))
	fmt.Fprintf(&b, "ID: %s\n", id)
	fmt.Fprintf(&b, "Status: %s\n", valueOr(opts.Status, "draft"))
	fmt.Fprintf(&b, "Scope: %s\n", valueOr(opts.Scope, "N/A"))
	b.WriteString("Links:\n")
	for _, key := range keys {
		values := links[key]
		if len(values) == 0 {
			values = []string{"N/A"}
		}
		fmt.Fprintf(&b, "- %s: %s\n", key, strings.Join(values, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func StampTypedSessionMetadata(body, sessionID, sessionSource string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	sessionSource = strings.TrimSpace(sessionSource)
	if sessionID == "" && sessionSource == "" {
		return body, nil
	}
	tc := ParseTypedComment(body)
	if !tc.HasHead {
		return "", errors.New("typed comment is missing visible header")
	}
	if len(tc.Errors) > 0 {
		return "", errors.New(strings.Join(tc.Errors, "; "))
	}
	lines := strings.Split(body, "\n")
	agentIndex := -1
	sessionIDIndex := -1
	sessionSourceIndex := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Agent:"):
			agentIndex = i
		case strings.HasPrefix(trimmed, "Agent Session ID:"):
			sessionIDIndex = i
		case strings.HasPrefix(trimmed, "Agent Session Source:"):
			sessionSourceIndex = i
		case strings.HasPrefix(trimmed, "Type:"):
			if agentIndex == -1 {
				return "", errors.New("typed comment is missing Agent")
			}
			if sessionSource != "" {
				lines = upsertHeaderLine(lines, &sessionSourceIndex, sessionIDIndex, agentIndex+1, "Agent Session Source: "+sessionSource)
			}
			if sessionID != "" {
				lines = upsertHeaderLine(lines, &sessionIDIndex, agentIndex, agentIndex+1, "Agent Session ID: "+sessionID)
			}
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", errors.New("typed comment is missing Type header")
}

func upsertHeaderLine(lines []string, index *int, afterIndex, fallbackIndex int, line string) []string {
	if *index >= 0 {
		lines[*index] = line
		return lines
	}
	insertAt := fallbackIndex
	if afterIndex >= 0 {
		insertAt = afterIndex + 1
	}
	if insertAt < 0 {
		insertAt = 0
	}
	if insertAt > len(lines) {
		insertAt = len(lines)
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:])
	lines[insertAt] = line
	*index = insertAt
	return lines
}

func IsLikelyTyped(body string) bool {
	return HasTypedMarker(body) || (strings.Contains(body, "Type:") && strings.Contains(body, "ID:") && strings.Contains(body, "Status:"))
}

func NormalizeURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func RelatedCommentURLs(tc TypedComment) []string {
	return filterURLValues(tc.Links["Related Comments"])
}

func LinkValues(tc TypedComment, name string) []string {
	return filterURLValues(tc.Links[name])
}

func parseLinkLine(line string) (string, string, bool) {
	line = strings.TrimPrefix(strings.TrimSpace(line), "- ")
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(name), strings.TrimSpace(value), true
}

func splitLinkValues(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "N/A") {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && !strings.EqualFold(part, "N/A") {
			out = append(out, part)
		}
	}
	return out
}

func filterURLValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.EqualFold(value, "N/A") {
			continue
		}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func defaultLinks(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, key := range []string{"Proposal Issue", "Design Issue", "Implement Issue", "Related Comments", "PR"} {
		out[key] = []string{"N/A"}
	}
	for key, values := range in {
		if len(values) == 0 {
			continue
		}
		out[key] = values
	}
	return out
}

func visibleMetadata(body string) map[string]string {
	out := map[string]string{}
	started := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<!--") {
			continue
		}
		if trimmed == "" {
			if started {
				break
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			if started {
				break
			}
			continue
		}
		started = true
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
