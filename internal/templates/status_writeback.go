package templates

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
)

const (
	RunnerStatusMarkerSchemaVersion = 1
	runnerStatusMarkerPrefix        = "<!-- issue-spec-runner:status "
	runnerStatusMarkerSuffix        = " -->"
)

type RunnerStatusMarker struct {
	SchemaVersion       int    `json:"schema_version"`
	StatusWritebackKey  string `json:"status_writeback_key,omitempty"`
	JobID               string `json:"job_id,omitempty"`
	PublicSessionID     string `json:"public_session_id,omitempty"`
	TriggerCommentID    int64  `json:"trigger_comment_id,omitempty"`
	TriggeringUserLogin string `json:"triggering_user_login,omitempty"`
	AgentKind           string `json:"agent_kind,omitempty"`
	Model               string `json:"model,omitempty"`
	StatusCommentID     int64  `json:"status_comment_id,omitempty"`
}

type RunnerStatusComment struct {
	Marker              RunnerStatusMarker
	Status              string
	Phase               string
	RunnerJobID         string
	PublicSessionID     string
	TriggerCommentID    int64
	TriggeringUserLogin string
	SessionCreatorLogin string
	CurrentUserLogin    string
	CancelingUserLogin  string
	AgentKind           string
	Model               string
	SandboxProvider     string
	FSBoundary          string
	UnsafeNoSandbox     bool
	CoordinatorSummary  *runnercontext.CoordinatorSummary
	CLIDirect           []RunnerCLICommand
	Diagnostics         []string
	Error               string
	MaxTextBytes        int
	MaxItems            int
}

type RunnerCLICommand struct {
	Name          string
	ExitCode      int
	ArtifactID    string
	ArtifactURL   string
	StdoutSummary string
	StderrSummary string
	Diagnostics   string
}

func RenderRunnerStatusComment(comment RunnerStatusComment) (string, error) {
	comment = normalizeRunnerStatusComment(comment)
	marker, err := renderRunnerStatusMarker(comment.Marker)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(marker)
	b.WriteString("\n\n### issue-spec runner status\n\n")
	b.WriteString("| Field | Value |\n| --- | --- |\n")
	tableRow(&b, "Status", codeOrNA(comment.Status))
	tableRow(&b, "Phase", codeOrNA(comment.Phase))
	tableRow(&b, "Runner job", codeOrNA(comment.RunnerJobID))
	tableRow(&b, "Public session", codeOrNA(comment.PublicSessionID))
	tableRow(&b, "Trigger comment", codeOrNA(formatInt(comment.TriggerCommentID)))
	tableRow(&b, "Triggering user", codeOrNA(comment.TriggeringUserLogin))
	tableRow(&b, "Session creator", codeOrNA(comment.SessionCreatorLogin))
	tableRow(&b, "Current turn user", codeOrNA(comment.CurrentUserLogin))
	if strings.TrimSpace(comment.CancelingUserLogin) != "" {
		tableRow(&b, "Canceling user", codeOrNA(comment.CancelingUserLogin))
	}
	tableRow(&b, "Agent", codeOrNA(joinNonEmpty(" / ", comment.AgentKind, comment.Model)))
	tableRow(&b, "Sandbox", codeOrNA(joinNonEmpty(" / ", comment.SandboxProvider, comment.FSBoundary)))
	if comment.UnsafeNoSandbox {
		tableRow(&b, "Sandbox warning", "unsafe no-sandbox mode requested; filesystem boundary disabled")
	}

	writeCoordinatorSummary(&b, comment)
	writeDiagnostics(&b, comment)
	return b.String(), nil
}

func ParseRunnerStatusMarker(body string) (RunnerStatusMarker, bool, error) {
	start := strings.Index(body, runnerStatusMarkerPrefix)
	if start < 0 {
		return RunnerStatusMarker{}, false, nil
	}
	start += len(runnerStatusMarkerPrefix)
	end := strings.Index(body[start:], runnerStatusMarkerSuffix)
	if end < 0 {
		return RunnerStatusMarker{}, true, fmt.Errorf("runner status marker is not closed")
	}
	var marker RunnerStatusMarker
	if err := json.Unmarshal([]byte(body[start:start+end]), &marker); err != nil {
		return RunnerStatusMarker{}, true, fmt.Errorf("parse runner status marker: %w", err)
	}
	if marker.SchemaVersion != RunnerStatusMarkerSchemaVersion {
		return RunnerStatusMarker{}, true, fmt.Errorf("unsupported runner status marker schema version %d", marker.SchemaVersion)
	}
	return marker, true, nil
}

func renderRunnerStatusMarker(marker RunnerStatusMarker) (string, error) {
	if marker.SchemaVersion == 0 {
		marker.SchemaVersion = RunnerStatusMarkerSchemaVersion
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return "", err
	}
	return runnerStatusMarkerPrefix + string(data) + runnerStatusMarkerSuffix, nil
}

func normalizeRunnerStatusComment(comment RunnerStatusComment) RunnerStatusComment {
	if comment.MaxTextBytes <= 0 {
		comment.MaxTextBytes = 2048
	}
	if comment.MaxItems <= 0 {
		comment.MaxItems = 10
	}
	if strings.TrimSpace(comment.Phase) == "" {
		comment.Phase = comment.Status
	}
	if strings.TrimSpace(comment.RunnerJobID) == "" {
		comment.RunnerJobID = comment.Marker.JobID
	}
	if strings.TrimSpace(comment.PublicSessionID) == "" {
		comment.PublicSessionID = comment.Marker.PublicSessionID
	}
	if comment.TriggerCommentID == 0 {
		comment.TriggerCommentID = comment.Marker.TriggerCommentID
	}
	if strings.TrimSpace(comment.TriggeringUserLogin) == "" {
		comment.TriggeringUserLogin = comment.Marker.TriggeringUserLogin
	}
	if strings.TrimSpace(comment.CurrentUserLogin) == "" {
		comment.CurrentUserLogin = comment.TriggeringUserLogin
	}
	if strings.TrimSpace(comment.AgentKind) == "" {
		comment.AgentKind = comment.Marker.AgentKind
	}
	if strings.TrimSpace(comment.Model) == "" {
		comment.Model = comment.Marker.Model
	}
	return comment
}

func writeCoordinatorSummary(b *strings.Builder, comment RunnerStatusComment) {
	if comment.CoordinatorSummary == nil && len(comment.CLIDirect) == 0 {
		return
	}
	b.WriteString("\n## Coordinator Summary\n\n")
	if comment.CoordinatorSummary != nil {
		fmt.Fprintf(b, "- Coordinator status: %s\n", codeOrNA(comment.CoordinatorSummary.Status))
		for i, artifact := range limited(comment.CoordinatorSummary.Artifacts, comment.MaxItems) {
			line := joinNonEmpty(" ", artifact.Kind, artifact.ID, artifact.Action, artifact.URL)
			fmt.Fprintf(b, "- Coordinator-reported CLI artifact %d: %s\n", i+1, inlineText(line, comment.MaxTextBytes))
		}
		for i, command := range limited(comment.CoordinatorSummary.Commands, comment.MaxItems) {
			line := fmt.Sprintf("%s exit=%d", command.Name, command.ExitCode)
			if command.ArtifactID != "" || command.ArtifactURL != "" {
				line += " artifact=" + joinNonEmpty(" ", command.ArtifactID, command.ArtifactURL)
			}
			fmt.Fprintf(b, "- Coordinator CLI command %d: %s\n", i+1, inlineText(line, comment.MaxTextBytes))
		}
		for i, child := range limited(comment.CoordinatorSummary.Children, comment.MaxItems) {
			line := joinNonEmpty(" ", child.ID, child.Role, child.ProcessID, child.TaskID, child.Status, child.Evidence)
			fmt.Fprintf(b, "- Child provenance %d: %s\n", i+1, inlineText(line, comment.MaxTextBytes))
		}
		for i, process := range limited(comment.CoordinatorSummary.Processes, comment.MaxItems) {
			line := joinNonEmpty(" ", process.ProcessID, process.TaskID, process.Status, process.Evidence)
			fmt.Fprintf(b, "- PROCESS evidence %d: %s\n", i+1, inlineText(line, comment.MaxTextBytes))
		}
		for i, diagnostic := range limited(comment.CoordinatorSummary.Diagnostics, comment.MaxItems) {
			line := joinNonEmpty(": ", diagnostic.Severity, diagnostic.Message)
			fmt.Fprintf(b, "- Coordinator diagnostic %d: %s\n", i+1, inlineText(line, comment.MaxTextBytes))
		}
	}
	for i, command := range limited(comment.CLIDirect, comment.MaxItems) {
		line := fmt.Sprintf("%s exit=%d", command.Name, command.ExitCode)
		if command.ArtifactID != "" || command.ArtifactURL != "" {
			line += " artifact=" + joinNonEmpty(" ", command.ArtifactID, command.ArtifactURL)
		}
		if command.StdoutSummary != "" {
			line += " stdout=" + command.StdoutSummary
		}
		if command.StderrSummary != "" {
			line += " stderr=" + command.StderrSummary
		}
		if command.Diagnostics != "" {
			line += " diagnostics=" + command.Diagnostics
		}
		fmt.Fprintf(b, "- Stored coordinator CLI provenance %d: %s\n", i+1, inlineText(line, comment.MaxTextBytes))
	}
	b.WriteString("\nThe runner records this as bounded provenance. Workflow artifacts are written by the sandboxed coordinator through existing issue-spec CLI commands.\n")
}

func writeDiagnostics(b *strings.Builder, comment RunnerStatusComment) {
	diagnostics := append([]string{}, comment.Diagnostics...)
	if strings.TrimSpace(comment.Error) != "" {
		diagnostics = append(diagnostics, "error: "+comment.Error)
	}
	if len(diagnostics) == 0 {
		return
	}
	b.WriteString("\n## Diagnostics\n\n")
	for _, diagnostic := range limited(diagnostics, comment.MaxItems) {
		fmt.Fprintf(b, "- %s\n", inlineText(diagnostic, comment.MaxTextBytes))
	}
}

func tableRow(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "| %s | %s |\n", escapeTable(key), escapeTable(value))
}

func codeOrNA(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "N/A"
	}
	return "`" + strings.ReplaceAll(cleanOneLine(value), "`", "'") + "`"
}

func inlineText(value string, maxBytes int) string {
	value = cleanOneLine(value)
	value = truncateUTF8(value, maxBytes)
	if value == "" {
		return "N/A"
	}
	return strings.ReplaceAll(value, "`", "'")
}

func cleanOneLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.Join(strings.Fields(value), " ")
	return strings.TrimSpace(value)
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value
	}
	for len([]byte(value)) > maxBytes-3 {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return "..."
		}
		value = value[:len(value)-size]
	}
	return value + "..."
}

func escapeTable(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}

func joinNonEmpty(sep string, values ...string) string {
	var out []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return strings.Join(out, sep)
}

func formatInt(value int64) string {
	if value == 0 {
		return ""
	}
	return fmt.Sprint(value)
}

func limited[T any](items []T, max int) []T {
	if max <= 0 || len(items) <= max {
		return items
	}
	return items[:max]
}
