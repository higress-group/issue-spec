package model

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	findingMarkerRe      = regexp.MustCompile(`(?s)<!--\s*issue-spec:finding\s+([^>]*)-->`)
	findingReplyMarkerRe = regexp.MustCompile(`(?s)<!--\s*issue-spec:finding-reply\s+([^>]*)-->`)
)

type FindingMarker struct {
	ID                 string `json:"id"`
	Severity           string `json:"severity"`
	Process            string `json:"process"`
	Spec               string `json:"spec"`
	Status             string `json:"status"`
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Agent              string `json:"agent,omitempty"`
	AgentSessionID     string `json:"agent_session_id,omitempty"`
	AgentSessionSource string `json:"agent_session_source,omitempty"`
	Version            int    `json:"version"`
}

type FindingReplyMarker struct {
	Finding            string `json:"finding"`
	Process            string `json:"process"`
	Status             string `json:"status"`
	Agent              string `json:"agent,omitempty"`
	AgentSessionID     string `json:"agent_session_id,omitempty"`
	AgentSessionSource string `json:"agent_session_source,omitempty"`
	Version            int    `json:"version"`
}

func RenderFindingMarker(id, severity, process, spec, status, path string, line int) string {
	return fmt.Sprintf("<!-- issue-spec:finding id=%s severity=%s process=%s spec=%s status=%s path=%s line=%d version=1 -->",
		strings.TrimSpace(id),
		NormalizeFindingSeverity(severity),
		strings.TrimSpace(process),
		strings.TrimSpace(spec),
		NormalizeFindingStatus(status),
		strings.TrimSpace(path),
		line,
	)
}

func FindFindingMarker(body string) (FindingMarker, bool, error) {
	matches := findingMarkerRe.FindAllStringSubmatch(body, -1)
	for _, match := range matches {
		attrs := parseMarkerAttrs(match[1])
		if attrs["id"] == "" {
			continue
		}
		line, err := positiveIntAttr(attrs["line"], "finding marker line")
		if err != nil {
			return FindingMarker{}, true, err
		}
		version, err := positiveIntAttr(defaultString(attrs["version"], "1"), "finding marker version")
		if err != nil {
			return FindingMarker{}, true, err
		}
		metadata := visibleMetadata(body)
		return FindingMarker{
			ID:                 attrs["id"],
			Severity:           NormalizeFindingSeverity(attrs["severity"]),
			Process:            attrs["process"],
			Spec:               attrs["spec"],
			Status:             NormalizeFindingStatus(attrs["status"]),
			Path:               attrs["path"],
			Line:               line,
			Agent:              metadata["Agent"],
			AgentSessionID:     metadata["Agent Session ID"],
			AgentSessionSource: metadata["Agent Session Source"],
			Version:            version,
		}, true, nil
	}
	return FindingMarker{}, false, nil
}

func RenderFindingBody(agent, id, severity, processID, specID, specURL, body, status, path string, line int) (string, error) {
	return RenderFindingBodyWithSession(agent, "", "", id, severity, processID, specID, specURL, body, status, path, line)
}

func RenderFindingBodyWithSession(agent, sessionID, sessionSource, id, severity, processID, specID, specURL, body, status, path string, line int) (string, error) {
	if strings.TrimSpace(agent) == "" {
		agent = "Review Agent"
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("finding id is required")
	}
	if strings.TrimSpace(processID) == "" {
		return "", fmt.Errorf("process id is required")
	}
	if strings.TrimSpace(specID) == "" {
		return "", fmt.Errorf("spec id is required")
	}
	if strings.TrimSpace(specURL) == "" {
		return "", fmt.Errorf("spec URL is required")
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("finding body is required")
	}
	severity = NormalizeFindingSeverity(severity)
	status = NormalizeFindingStatus(status)
	var sessionLines strings.Builder
	if strings.TrimSpace(sessionID) != "" {
		fmt.Fprintf(&sessionLines, "Agent Session ID: %s\n", strings.TrimSpace(sessionID))
	}
	if strings.TrimSpace(sessionSource) != "" {
		fmt.Fprintf(&sessionLines, "Agent Session Source: %s\n", strings.TrimSpace(sessionSource))
	}
	return fmt.Sprintf(`%s
Agent: %s
%sType: FINDING
ID: %s
Severity: %s
Status: %s
Process: %s
Spec: %s
Spec Comment: %s

%s
`, RenderFindingMarker(id, severity, processID, specID, status, path, line), agent, sessionLines.String(), id, severity, status, processID, specID, specURL, strings.TrimSpace(body)), nil
}

func RenderFindingReplyMarker(finding, process, status string) string {
	return fmt.Sprintf("<!-- issue-spec:finding-reply finding=%s process=%s status=%s version=1 -->",
		strings.TrimSpace(finding),
		strings.TrimSpace(process),
		NormalizeFindingStatus(status),
	)
}

func FindFindingReplyMarker(body string) (FindingReplyMarker, bool, error) {
	matches := findingReplyMarkerRe.FindAllStringSubmatch(body, -1)
	for _, match := range matches {
		attrs := parseMarkerAttrs(match[1])
		if attrs["finding"] == "" {
			continue
		}
		version, err := positiveIntAttr(defaultString(attrs["version"], "1"), "finding reply marker version")
		if err != nil {
			return FindingReplyMarker{}, true, err
		}
		metadata := visibleMetadata(body)
		return FindingReplyMarker{
			Finding:            attrs["finding"],
			Process:            attrs["process"],
			Status:             NormalizeFindingStatus(attrs["status"]),
			Agent:              metadata["Agent"],
			AgentSessionID:     metadata["Agent Session ID"],
			AgentSessionSource: metadata["Agent Session Source"],
			Version:            version,
		}, true, nil
	}
	return FindingReplyMarker{}, false, nil
}

func RenderFindingReplyBody(agent, findingID, processID, status, body string) (string, error) {
	return RenderFindingReplyBodyWithSession(agent, "", "", findingID, processID, status, body)
}

func RenderFindingReplyBodyWithSession(agent, sessionID, sessionSource, findingID, processID, status, body string) (string, error) {
	if strings.TrimSpace(agent) == "" {
		agent = "Worker Agent"
	}
	findingID = strings.TrimSpace(findingID)
	if findingID == "" {
		return "", fmt.Errorf("finding id is required")
	}
	if strings.TrimSpace(processID) == "" {
		return "", fmt.Errorf("process id is required")
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("reply body is required")
	}
	status = NormalizeFindingStatus(status)
	var sessionLines strings.Builder
	if strings.TrimSpace(sessionID) != "" {
		fmt.Fprintf(&sessionLines, "Agent Session ID: %s\n", strings.TrimSpace(sessionID))
	}
	if strings.TrimSpace(sessionSource) != "" {
		fmt.Fprintf(&sessionLines, "Agent Session Source: %s\n", strings.TrimSpace(sessionSource))
	}
	return fmt.Sprintf(`%s
Agent: %s
%sType: FINDING_REPLY
Finding: %s
Status: %s
Process: %s

%s
`, RenderFindingReplyMarker(findingID, processID, status), agent, sessionLines.String(), findingID, status, processID, strings.TrimSpace(body)), nil
}

func SameFinding(existing FindingMarker, id, path string, line int) bool {
	return existing.ID == strings.TrimSpace(id) && existing.Path == strings.TrimSpace(path) && existing.Line == line
}

func NormalizeFindingSeverity(severity string) string {
	severity = strings.ToUpper(strings.TrimSpace(severity))
	switch severity {
	case "P0", "P1", "P2":
		return severity
	default:
		return "P2"
	}
}

func NormalizeFindingStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "open", "resolved", "done", "closed", "fixed", "superseded":
		return status
	default:
		return "open"
	}
}

func IsTerminalFindingStatus(status string) bool {
	switch NormalizeFindingStatus(status) {
	case "resolved", "done", "closed", "fixed", "superseded":
		return true
	default:
		return false
	}
}

func positiveIntAttr(value, name string) (int, error) {
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, value)
	}
	return n, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
