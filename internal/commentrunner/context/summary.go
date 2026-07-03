package contextbundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type SummaryBounds struct {
	MaxOutputBytes     int `json:"max_output_bytes"`
	MaxDiagnosticBytes int `json:"max_diagnostic_bytes"`
}

func DefaultSummaryBounds() SummaryBounds {
	return SummaryBounds{
		MaxOutputBytes:     4096,
		MaxDiagnosticBytes: 4096,
	}
}

type CoordinatorSummary struct {
	Status      string              `json:"status"`
	Artifacts   []WorkflowArtifact  `json:"artifacts,omitempty"`
	Commands    []CLICommandSummary `json:"commands,omitempty"`
	Children    []ChildSummary      `json:"children,omitempty"`
	Processes   []ProcessEvidence   `json:"processes,omitempty"`
	Diagnostics []DiagnosticSummary `json:"diagnostics,omitempty"`
}

type WorkflowArtifact struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	URL       string `json:"url,omitempty"`
	Issue     int    `json:"issue,omitempty"`
	CommentID int64  `json:"comment_id,omitempty"`
	Action    string `json:"action,omitempty"`
}

type CLICommandSummary struct {
	Name          string `json:"name"`
	ExitCode      int    `json:"exit_code"`
	ArtifactID    string `json:"artifact_id,omitempty"`
	ArtifactURL   string `json:"artifact_url,omitempty"`
	StdoutSummary string `json:"stdout_summary,omitempty"`
	StderrSummary string `json:"stderr_summary,omitempty"`
	Diagnostics   string `json:"diagnostics,omitempty"`
}

type ChildSummary struct {
	ID        string `json:"id"`
	NativeID  string `json:"native_id,omitempty"`
	Role      string `json:"role,omitempty"`
	ProcessID string `json:"process_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	Status    string `json:"status"`
	Evidence  string `json:"evidence,omitempty"`
}

type ProcessEvidence struct {
	ProcessID string `json:"process_id"`
	TaskID    string `json:"task_id,omitempty"`
	Status    string `json:"status"`
	Evidence  string `json:"evidence,omitempty"`
}

type DiagnosticSummary struct {
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

func (d *DiagnosticSummary) UnmarshalJSON(data []byte) error {
	var message string
	if err := json.Unmarshal(data, &message); err == nil {
		d.Severity = ""
		d.Message = message
		return nil
	}

	var object struct {
		Severity string `json:"severity,omitempty"`
		Level    string `json:"level,omitempty"`
		Message  string `json:"message"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&object); err != nil {
		return err
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("diagnostic contains multiple JSON values")
		}
		return err
	}
	d.Severity = object.Severity
	if d.Severity == "" {
		d.Severity = object.Level
	}
	d.Message = object.Message
	return nil
}

type CoordinatorSummaryBlock struct {
	Start int
	End   int
	Body  string
}

func ParseCoordinatorSummary(data []byte, bounds SummaryBounds) (CoordinatorSummary, error) {
	bounds = normalizeSummaryBounds(bounds)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var summary CoordinatorSummary
	if err := dec.Decode(&summary); err != nil {
		return CoordinatorSummary{}, err
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return CoordinatorSummary{}, fmt.Errorf("summary contains multiple JSON values")
		}
		return CoordinatorSummary{}, err
	}
	if err := ValidateCoordinatorSummary(summary, bounds); err != nil {
		return CoordinatorSummary{}, err
	}
	return summary, nil
}

func ExtractCoordinatorSummary(reply string, bounds SummaryBounds) (CoordinatorSummary, bool, error) {
	blocks, err := FindCoordinatorSummaryBlocks(reply)
	if err != nil {
		return CoordinatorSummary{}, true, err
	}
	if len(blocks) == 0 {
		return CoordinatorSummary{}, false, nil
	}
	if len(blocks) > 1 {
		return CoordinatorSummary{}, true, fmt.Errorf("multiple coordinator summaries found")
	}
	body := strings.TrimSpace(blocks[0].Body)
	if body == "" {
		return CoordinatorSummary{}, true, fmt.Errorf("coordinator summary fence has no body")
	}
	summary, err := ParseCoordinatorSummary([]byte(body), bounds)
	if err != nil {
		return CoordinatorSummary{}, true, err
	}
	return summary, true, nil
}

func FindCoordinatorSummaryBlocks(text string) ([]CoordinatorSummaryBlock, error) {
	const fence = "```issue_spec_coordinator_summary"
	var blocks []CoordinatorSummaryBlock
	offset := 0
	for offset < len(text) {
		startRel := strings.Index(text[offset:], fence)
		if startRel == -1 {
			break
		}
		start := offset + startRel
		lineEnd := strings.IndexByte(text[start:], '\n')
		if lineEnd == -1 {
			lineEnd = len(text)
		} else {
			lineEnd += start + 1
		}
		line := strings.TrimSpace(strings.TrimSuffix(text[start:lineEnd], "\n"))
		bodyPrefix, ok := coordinatorSummaryBodyPrefix(line)
		if ok {
			closeStart, closeEnd, closed := findClosingSummaryFence(text, lineEnd)
			if !closed {
				return nil, fmt.Errorf("coordinator summary fence is not closed")
			}
			blocks = append(blocks, CoordinatorSummaryBlock{
				Start: start,
				End:   closeEnd,
				Body:  bodyPrefix + text[lineEnd:closeStart],
			})
			offset = closeEnd
			continue
		}
		offset = start + len(fence)
	}
	return blocks, nil
}

func coordinatorSummaryBodyPrefix(line string) (string, bool) {
	const fence = "```issue_spec_coordinator_summary"
	if !strings.HasPrefix(line, fence) {
		return "", false
	}
	suffix := strings.TrimSpace(strings.TrimPrefix(line, fence))
	if suffix == "" {
		return "", true
	}
	if strings.HasPrefix(suffix, "{") {
		return suffix, true
	}
	return "", false
}

func findClosingSummaryFence(text string, offset int) (int, int, bool) {
	for offset < len(text) {
		lineEnd := strings.IndexByte(text[offset:], '\n')
		if lineEnd == -1 {
			lineEnd = len(text)
		} else {
			lineEnd += offset + 1
		}
		line := strings.TrimSpace(strings.TrimSuffix(text[offset:lineEnd], "\n"))
		if strings.HasPrefix(line, "```") {
			return offset, lineEnd, true
		}
		offset = lineEnd
	}
	return 0, 0, false
}

func ValidateCoordinatorSummary(summary CoordinatorSummary, bounds SummaryBounds) error {
	bounds = normalizeSummaryBounds(bounds)
	switch summary.Status {
	case "completed", "failed", "partial":
	default:
		return fmt.Errorf("summary status must be completed, failed, or partial")
	}
	for i, artifact := range summary.Artifacts {
		if strings.TrimSpace(artifact.Kind) == "" {
			return fmt.Errorf("artifact %d kind is required", i)
		}
		if strings.TrimSpace(artifact.ID) == "" && strings.TrimSpace(artifact.URL) == "" && artifact.Issue == 0 && artifact.CommentID == 0 {
			return fmt.Errorf("artifact %d must include an id, URL, issue, or comment id", i)
		}
	}
	for i, command := range summary.Commands {
		if strings.TrimSpace(command.Name) == "" {
			return fmt.Errorf("command %d name is required", i)
		}
		if command.ExitCode < 0 {
			return fmt.Errorf("command %d exit_code must be non-negative", i)
		}
		if len([]byte(command.StdoutSummary)) > bounds.MaxOutputBytes {
			return fmt.Errorf("command %d stdout_summary exceeds limit", i)
		}
		if len([]byte(command.StderrSummary)) > bounds.MaxOutputBytes {
			return fmt.Errorf("command %d stderr_summary exceeds limit", i)
		}
		if len([]byte(command.Diagnostics)) > bounds.MaxDiagnosticBytes {
			return fmt.Errorf("command %d diagnostics exceed limit", i)
		}
	}
	for i, child := range summary.Children {
		if strings.TrimSpace(child.ID) == "" {
			return fmt.Errorf("child %d id is required", i)
		}
		if strings.TrimSpace(child.Status) == "" {
			return fmt.Errorf("child %d status is required", i)
		}
		if len([]byte(child.Evidence)) > bounds.MaxDiagnosticBytes {
			return fmt.Errorf("child %d evidence exceeds limit", i)
		}
	}
	for i, process := range summary.Processes {
		if strings.TrimSpace(process.ProcessID) == "" {
			return fmt.Errorf("process %d process_id is required", i)
		}
		if strings.TrimSpace(process.Status) == "" {
			return fmt.Errorf("process %d status is required", i)
		}
		if len([]byte(process.Evidence)) > bounds.MaxDiagnosticBytes {
			return fmt.Errorf("process %d evidence exceeds limit", i)
		}
	}
	for i, diagnostic := range summary.Diagnostics {
		if strings.TrimSpace(diagnostic.Message) == "" {
			return fmt.Errorf("diagnostic %d message is required", i)
		}
		if len([]byte(diagnostic.Message)) > bounds.MaxDiagnosticBytes {
			return fmt.Errorf("diagnostic %d message exceeds limit", i)
		}
	}
	return nil
}

func normalizeSummaryBounds(bounds SummaryBounds) SummaryBounds {
	defaults := DefaultSummaryBounds()
	if bounds.MaxOutputBytes <= 0 {
		bounds.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if bounds.MaxDiagnosticBytes <= 0 {
		bounds.MaxDiagnosticBytes = defaults.MaxDiagnosticBytes
	}
	return bounds
}
