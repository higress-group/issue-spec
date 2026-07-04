package commands

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

type metadataDiagnostic struct {
	Level    string `json:"level"`
	Code     string `json:"code"`
	Artifact string `json:"artifact"`
	URL      string `json:"url,omitempty"`
	Message  string `json:"message"`
}

func typedSessionDiagnostics(artifacts []model.Artifact) []metadataDiagnostic {
	var out []metadataDiagnostic
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type == "" || strings.TrimSpace(tc.Agent) == "" {
			continue
		}
		out = append(out, artifactSessionDiagnostics(tc.Type+"/"+tc.ID, artifact.URL, tc.AgentSessionID, tc.AgentSessionSource)...)
	}
	return out
}

func artifactSessionDiagnostics(artifact, url, sessionID, sessionSource string) []metadataDiagnostic {
	sessionID = strings.TrimSpace(sessionID)
	sessionSource = strings.TrimSpace(sessionSource)
	switch {
	case sessionID == "" && sessionSource == "":
		return []metadataDiagnostic{{
			Level:    "warning",
			Code:     "missing_session_metadata",
			Artifact: artifact,
			URL:      url,
			Message:  fmt.Sprintf("%s has logical agent metadata but lacks Agent Session ID and Agent Session Source", artifact),
		}}
	case sessionID == "" || sessionSource == "":
		return []metadataDiagnostic{{
			Level:    "warning",
			Code:     "partial_session_metadata",
			Artifact: artifact,
			URL:      url,
			Message:  fmt.Sprintf("%s has partial artifact writer session metadata", artifact),
		}}
	case sessionSource != codexThreadIDEnv && sessionSource != agentSessionParamSource:
		return []metadataDiagnostic{{
			Level:    "warning",
			Code:     "invalid_session_source",
			Artifact: artifact,
			URL:      url,
			Message:  fmt.Sprintf("%s has unrecognized Agent Session Source %q", artifact, sessionSource),
		}}
	default:
		return nil
	}
}
