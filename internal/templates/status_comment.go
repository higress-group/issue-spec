package templates

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

type StatusCommentOptions struct {
	JobID       string
	Agent       string
	State       string
	Command     string
	Provenance  string
	Diagnostics []string
	Interrupted bool
	Cancelled   bool
}

func StatusComment(opts StatusCommentOptions) (string, error) {
	jobID := strings.TrimSpace(opts.JobID)
	if jobID == "" {
		return "", fmt.Errorf("job id is required")
	}
	state := strings.TrimSpace(opts.State)
	if state == "" {
		state = "queued"
	}
	header := model.RenderHeader("PROCESS", jobID, model.BodyOptions{
		Agent:  valueOr(opts.Agent, "Worker"),
		Status: state,
		Scope:  "job lifecycle",
		Links:  map[string][]string{},
	})
	var b strings.Builder
	b.WriteString(renderStatusMarker(jobID))
	b.WriteString("\n")
	b.WriteString(header)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "## Status\n\n%s\n\n", renderStatusLabel(state, opts.Interrupted, opts.Cancelled))
	if cmd := strings.TrimSpace(opts.Command); cmd != "" {
		fmt.Fprintf(&b, "## Command\n\n%s\n\n", cmd)
	}
	if prov := strings.TrimSpace(opts.Provenance); prov != "" {
		fmt.Fprintf(&b, "## Provenance\n\n%s\n\n", prov)
	}
	b.WriteString("## Diagnostics\n\n")
	for _, line := range boundDiagnostics(opts.Diagnostics, 8, 120) {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	if len(boundDiagnostics(opts.Diagnostics, 8, 120)) == 0 {
		b.WriteString("- None.\n")
	}
	return b.String(), nil
}

func renderStatusMarker(jobID string) string {
	return fmt.Sprintf("<!-- issue-spec:status job=%s version=1 -->", strings.TrimSpace(jobID))
}

func renderStatusLabel(state string, interrupted, cancelled bool) string {
	switch {
	case cancelled:
		return "Cancelled."
	case interrupted:
		return "Interrupted."
	case state == "done":
		return "Completed."
	case state == "blocked":
		return "Blocked."
	default:
		return strings.ToUpper(state[:1]) + state[1:] + "."
	}
}

func boundDiagnostics(lines []string, maxLines, maxLen int) []string {
	if maxLines <= 0 || maxLen <= 0 {
		return nil
	}
	out := make([]string, 0, min(len(lines), maxLines))
	for _, line := range lines {
		if len(out) == maxLines {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > maxLen {
			line = line[:maxLen-1] + "…"
		}
		out = append(out, line)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
