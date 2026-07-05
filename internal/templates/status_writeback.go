package templates

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
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
	if strings.TrimSpace(comment.PublicSessionID) != "" {
		tableRow(&b, "Public session", codeOrNA(comment.PublicSessionID))
	}

	writeResultSummary(&b, comment)
	writeRejectedReason(&b, comment)
	writeResumeGuidance(&b, comment)
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
	publicMarker := struct {
		SchemaVersion      int    `json:"schema_version"`
		StatusWritebackKey string `json:"status_writeback_key,omitempty"`
	}{
		SchemaVersion:      marker.SchemaVersion,
		StatusWritebackKey: marker.StatusWritebackKey,
	}
	data, err := json.Marshal(publicMarker)
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

func writeResultSummary(b *strings.Builder, comment RunnerStatusComment) {
	if comment.CoordinatorSummary == nil && len(comment.CLIDirect) == 0 {
		return
	}
	if comment.CoordinatorSummary == nil && !crstate.LifecycleStatus(strings.TrimSpace(comment.Status)).Terminal() {
		return
	}
	b.WriteString("\n## Result\n\n")
	if comment.CoordinatorSummary != nil {
		fmt.Fprintf(b, "- %s\n", publicStatusSummary(comment.CoordinatorSummary.Status))
		seen := map[string]bool{}
		for _, artifact := range limited(comment.CoordinatorSummary.Artifacts, comment.MaxItems) {
			writePublicArtifactLine(b, publicWorkflowArtifactLine(artifact), comment.MaxTextBytes, seen)
		}
		for _, command := range limited(comment.CoordinatorSummary.Commands, comment.MaxItems) {
			writePublicArtifactLine(b, publicCoordinatorCommandArtifactLine(command), comment.MaxTextBytes, seen)
		}
		return
	}

	fmt.Fprintf(b, "- %s\n", publicStatusSummary(comment.Status))
	seen := map[string]bool{}
	for _, command := range limited(comment.CLIDirect, comment.MaxItems) {
		writePublicArtifactLine(b, publicCLIArtifactLine(command), comment.MaxTextBytes, seen)
	}
}

func publicStatusSummary(status string) string {
	switch strings.TrimSpace(status) {
	case "completed":
		return "Completed the requested command."
	case "partial":
		return "Partially completed the requested command."
	case "failed":
		return "The requested command did not complete successfully."
	case "cancelled":
		return "Cancelled the requested command."
	case "interrupted":
		return "The requested command was interrupted."
	case "rejected":
		return "Rejected the requested command."
	default:
		return "Processed the requested command."
	}
}

type publicArtifactLine struct {
	Line string
	Keys []string
}

func publicWorkflowArtifactLine(artifact runnercontext.WorkflowArtifact) publicArtifactLine {
	line := joinNonEmpty(" ", artifact.Action, artifact.Kind, artifact.ID)
	if line == "" {
		line = "workflow artifact"
	}
	keys := publicArtifactKeys(artifact.URL, artifact.ID)
	if artifact.Issue != 0 {
		keys = append(keys, fmt.Sprintf("issue:%d", artifact.Issue))
	}
	if artifact.CommentID != 0 {
		keys = append(keys, fmt.Sprintf("comment:%d", artifact.CommentID))
	}
	if strings.TrimSpace(artifact.URL) != "" {
		return publicArtifactLine{Line: line + ": " + strings.TrimSpace(artifact.URL), Keys: keys}
	}
	var refs []string
	if artifact.Issue != 0 {
		refs = append(refs, "#"+formatInt(int64(artifact.Issue)))
	}
	if artifact.CommentID != 0 {
		refs = append(refs, "comment "+formatInt(artifact.CommentID))
	}
	if len(refs) > 0 {
		return publicArtifactLine{Line: joinNonEmpty(" ", line, strings.Join(refs, " ")), Keys: keys}
	}
	return publicArtifactLine{Line: line, Keys: keys}
}

func publicCoordinatorCommandArtifactLine(command runnercontext.CLICommandSummary) publicArtifactLine {
	if strings.TrimSpace(command.ArtifactID) == "" && strings.TrimSpace(command.ArtifactURL) == "" {
		return publicArtifactLine{}
	}
	line := joinNonEmpty(" ", "workflow artifact", command.ArtifactID)
	keys := publicArtifactKeys(command.ArtifactURL, command.ArtifactID)
	if strings.TrimSpace(command.ArtifactURL) != "" {
		return publicArtifactLine{Line: line + ": " + strings.TrimSpace(command.ArtifactURL), Keys: keys}
	}
	return publicArtifactLine{Line: line, Keys: keys}
}

func publicCLIArtifactLine(command RunnerCLICommand) publicArtifactLine {
	if strings.TrimSpace(command.ArtifactID) == "" && strings.TrimSpace(command.ArtifactURL) == "" {
		return publicArtifactLine{}
	}
	line := joinNonEmpty(" ", "workflow artifact", command.ArtifactID)
	keys := publicArtifactKeys(command.ArtifactURL, command.ArtifactID)
	if strings.TrimSpace(command.ArtifactURL) != "" {
		return publicArtifactLine{Line: line + ": " + strings.TrimSpace(command.ArtifactURL), Keys: keys}
	}
	return publicArtifactLine{Line: line, Keys: keys}
}

func publicArtifactKeys(url, id string) []string {
	var keys []string
	if value := cleanOneLine(url); value != "" {
		keys = append(keys, "url:"+value)
	}
	if value := cleanOneLine(id); value != "" {
		keys = append(keys, "id:"+value)
	}
	return keys
}

func writePublicArtifactLine(b *strings.Builder, item publicArtifactLine, maxTextBytes int, seen map[string]bool) {
	line := inlineText(item.Line, maxTextBytes)
	if line == "N/A" || seen[line] {
		return
	}
	keys := append([]string{}, item.Keys...)
	if len(keys) == 0 {
		keys = append(keys, "line:"+line)
	}
	for _, key := range keys {
		if seen[key] {
			return
		}
	}
	seen[line] = true
	for _, key := range keys {
		seen[key] = true
	}
	fmt.Fprintf(b, "- %s\n", line)
}

func writeRejectedReason(b *strings.Builder, comment RunnerStatusComment) {
	if strings.TrimSpace(comment.Status) != "rejected" {
		return
	}
	diagnostics := append([]string{}, comment.Diagnostics...)
	if strings.TrimSpace(comment.Error) != "" {
		diagnostics = append(diagnostics, "error: "+comment.Error)
	}
	seen := map[string]bool{}
	var lines []string
	for _, diagnostic := range limited(diagnostics, comment.MaxItems) {
		line := inlineText(diagnostic, comment.MaxTextBytes)
		if line == "N/A" || seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("\n## Reason\n\n")
	for _, line := range lines {
		fmt.Fprintf(b, "- %s\n", line)
	}
}

func writeResumeGuidance(b *strings.Builder, comment RunnerStatusComment) {
	sessionID := strings.TrimSpace(comment.PublicSessionID)
	if sessionID == "" || !crstate.LifecycleStatus(strings.TrimSpace(comment.Status)).Terminal() {
		return
	}
	b.WriteString("\n## Continue Session\n\n")
	b.WriteString("To continue this public session, create a new issue comment that starts with:\n\n")
	b.WriteString("```text\n")
	fmt.Fprintf(b, "/resume %s <answer or next instruction>\n", cleanOneLine(sessionID))
	b.WriteString("```\n\n")
	b.WriteString("Ordinary follow-up comments are recorded on GitHub, but the runner only sends `/resume` command comments to the coordinator.\n")
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
