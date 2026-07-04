package model

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var rationaleMarkerRe = regexp.MustCompile(`(?s)<!--\s*issue-spec:rationale\s+([^>]*)-->`)

type RationaleMarker struct {
	Process            string `json:"process"`
	Spec               string `json:"spec"`
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Agent              string `json:"agent,omitempty"`
	AgentSessionID     string `json:"agent_session_id,omitempty"`
	AgentSessionSource string `json:"agent_session_source,omitempty"`
	Version            int    `json:"version"`
}

func RenderRationaleMarker(process, spec, path string, line int) string {
	return fmt.Sprintf("<!-- issue-spec:rationale process=%s spec=%s path=%s line=%d version=1 -->", strings.TrimSpace(process), strings.TrimSpace(spec), strings.TrimSpace(path), line)
}

func FindRationaleMarker(body string) (RationaleMarker, bool, error) {
	matches := rationaleMarkerRe.FindAllStringSubmatch(body, -1)
	for _, match := range matches {
		attrs := parseMarkerAttrs(match[1])
		if attrs["process"] == "" && attrs["spec"] == "" {
			continue
		}
		line := 0
		if attrs["line"] != "" {
			n, err := strconv.Atoi(attrs["line"])
			if err != nil || n <= 0 {
				return RationaleMarker{}, true, fmt.Errorf("invalid rationale marker line %q", attrs["line"])
			}
			line = n
		}
		version := 1
		if attrs["version"] != "" {
			n, err := strconv.Atoi(attrs["version"])
			if err != nil || n <= 0 {
				return RationaleMarker{}, true, fmt.Errorf("invalid rationale marker version %q", attrs["version"])
			}
			version = n
		}
		return RationaleMarker{
			Process:            attrs["process"],
			Spec:               attrs["spec"],
			Path:               attrs["path"],
			Line:               line,
			Agent:              visibleMetadata(body)["Agent"],
			AgentSessionID:     visibleMetadata(body)["Agent Session ID"],
			AgentSessionSource: visibleMetadata(body)["Agent Session Source"],
			Version:            version,
		}, true, nil
	}
	return RationaleMarker{}, false, nil
}

func RenderRationaleBody(agent, processID, specID, specURL, body string, path string, line int) (string, error) {
	return RenderRationaleBodyWithSession(agent, "", "", processID, specID, specURL, body, path, line)
}

func RenderRationaleBodyWithSession(agent, sessionID, sessionSource, processID, specID, specURL, body string, path string, line int) (string, error) {
	if strings.TrimSpace(agent) == "" {
		agent = "Worker Agent"
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
		return "", fmt.Errorf("rationale body is required")
	}
	var sessionLines strings.Builder
	if strings.TrimSpace(sessionID) != "" {
		fmt.Fprintf(&sessionLines, "Agent Session ID: %s\n", strings.TrimSpace(sessionID))
	}
	if strings.TrimSpace(sessionSource) != "" {
		fmt.Fprintf(&sessionLines, "Agent Session Source: %s\n", strings.TrimSpace(sessionSource))
	}
	return fmt.Sprintf(`%s
Agent: %s
%sType: RATIONALE
Process: %s
Spec: %s
Spec Comment: %s

%s
`, RenderRationaleMarker(processID, specID, path, line), agent, sessionLines.String(), processID, specID, specURL, strings.TrimSpace(body)), nil
}

func SameRationale(existing RationaleMarker, process, spec, path string, line int) bool {
	return existing.Process == process && existing.Spec == spec && existing.Path == path && existing.Line == line
}
