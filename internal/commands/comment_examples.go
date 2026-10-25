package commands

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/durable"
	"github.com/higress-group/issue-spec/internal/templates"
)

// Use the generator's input types so examples follow the installed CLI's JSON
// contract. These are editable starting points, independent of project config.
func typedCommentInputExample(commentType, id string) (any, error) {
	switch strings.ToUpper(strings.TrimSpace(commentType)) {
	case "SPEC":
		id = strings.TrimSpace(id)
		if id == "" {
			id = "SPEC-1001"
		}
		const title = "health status endpoint"
		input := templates.SpecInput{
			Requirement: templates.SpecRequirementInput{
				Title: title,
				Text:  "The service MUST respond to GET /health with HTTP 200 when it is ready to serve requests.",
			},
			Scenarios: []templates.SpecScenarioInput{{
				Title: "ready service reports healthy",
				When:  "a client sends GET /health to a ready service",
				Then:  "the service responds with HTTP 200",
			}},
			Durable: &durable.Intent{
				Version: durable.IntentVersion,
				Kind:    durable.IntentOperations,
				Operations: []durable.Operation{{
					ID:             id + "-OP-01",
					Kind:           durable.OperationAdded,
					Capability:     "service-health",
					Path:           "issue-spec/specs/service-health/spec.md",
					NewRequirement: title,
					Projection:     &durable.Projection{Source: durable.ProjectionCurrentSpec},
				}},
			},
		}
		if err := input.Durable.Validate(durable.ValidationOptions{SpecID: id, SpecRequirement: title}); err != nil {
			return nil, err
		}
		return input, nil
	case "TASK":
		return templates.TaskInput{
			Title:   "implement the health status endpoint",
			Summary: "Expose service readiness through GET /health.",
			Checklist: []string{
				"Register GET /health in the service router",
				"Test that a ready service returns HTTP 200",
			},
			Covers: []string{"SPEC-1001"},
			ExecutionPlanning: templates.TaskExecutionPlanning{
				OwnedAreas:        []string{"internal/health/**"},
				SharedTouchpoints: []string{"internal/server/routes.go"},
				Dependencies:      []string{"Use the existing service router and readiness state"},
				Coupling:          "low: one endpoint uses the existing readiness interface",
				ExecutionMode:     "coordinator-owned",
				Complexity:        "small: implement and test the endpoint in one task",
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported --type %q for --example; supported types: SPEC, TASK", commentType)
	}
}
