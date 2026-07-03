package context

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/templates"
)

type SummaryRecord struct {
	ArtifactID  string   `json:"artifact_id,omitempty"`
	ArtifactURL string   `json:"artifact_url,omitempty"`
	CommandName string   `json:"command_name,omitempty"`
	ExitCode    int      `json:"exit_code,omitempty"`
	Stdout      string   `json:"stdout,omitempty"`
	Stderr      string   `json:"stderr,omitempty"`
	ChildIDs    []string `json:"child_ids,omitempty"`
	ProcessIDs  []string `json:"process_ids,omitempty"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

type SummarySchema struct {
	Records []SummaryRecord `json:"records"`
}

func RenderCoordinatorPrompt(bundle ContextBundle) (string, error) {
	payload, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(templates.CoordinatorPromptPolicy())
	b.WriteString("\n\n")
	b.WriteString("Bundle:\n")
	b.Write(payload)
	b.WriteString("\n\nSummary schema:\n")
	b.WriteString(templates.CoordinatorSummarySchema())
	return b.String(), nil
}

func ParseSummarySchema(input string) (SummarySchema, error) {
	var schema SummarySchema
	if err := json.Unmarshal([]byte(input), &schema); err != nil {
		return SummarySchema{}, err
	}
	for i, record := range schema.Records {
		if strings.TrimSpace(record.ArtifactID) == "" && strings.TrimSpace(record.CommandName) == "" {
			return SummarySchema{}, fmt.Errorf("record %d missing artifact_id or command_name", i)
		}
	}
	return schema, nil
}
