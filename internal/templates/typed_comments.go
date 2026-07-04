package templates

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

type QuestionOptions struct {
	ID                 string
	Agent              string
	AgentSessionID     string
	AgentSessionSource string
	Status             string
	Scope              string
	Blocking           bool
	Question           string
	Assumption         string
	Links              map[string][]string
}

func QuestionComment(opts QuestionOptions) (string, error) {
	if strings.TrimSpace(opts.Assumption) == "" {
		opts.Assumption = "N/A"
	}
	if strings.TrimSpace(opts.Status) == "" {
		if opts.Blocking {
			opts.Status = "blocked"
		} else {
			opts.Status = "draft"
		}
	}
	header := model.RenderHeader("QUESTION", opts.ID, model.BodyOptions{
		Agent:              opts.Agent,
		AgentSessionID:     opts.AgentSessionID,
		AgentSessionSource: opts.AgentSessionSource,
		Status:             opts.Status,
		Scope:              opts.Scope,
		Links:              opts.Links,
	})
	body := fmt.Sprintf(`%s
%s

## Question

%s

## Blocking

%t

## Default Assumption

%s

## Resolution Log

- Pending.
`, model.RenderMarker("QUESTION", opts.ID, 1), header, strings.TrimSpace(opts.Question), opts.Blocking, strings.TrimSpace(opts.Assumption))
	return model.EnsureTypedBody("QUESTION", opts.ID, body, model.BodyOptions{Agent: opts.Agent, AgentSessionID: opts.AgentSessionID, AgentSessionSource: opts.AgentSessionSource, Status: opts.Status, Scope: opts.Scope, Links: opts.Links})
}
