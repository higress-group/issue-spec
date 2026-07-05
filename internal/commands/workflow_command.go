package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/workflow"
)

type workflowCommandResult struct {
	OK       bool          `json:"ok"`
	Repo     string        `json:"repo"`
	Workflow workflow.Plan `json:"workflow"`
	Error    string        `json:"error,omitempty"`
}

func (a *app) runWorkflow(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec workflow validate|which ...\n")
		return 2
	}
	switch args[0] {
	case "validate":
		return a.runWorkflowInspect(ctx, args[1:], false)
	case "which":
		return a.runWorkflowInspect(ctx, args[1:], true)
	default:
		a.errorf("unknown workflow command %q\n", args[0])
		return 2
	}
}

func (a *app) runWorkflowInspect(_ context.Context, args []string, which bool) int {
	name := "workflow validate"
	if which {
		name = "workflow which"
	}
	fs := newFlagSet(name, a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	schema := fs.String("schema", "", "schema name override for diagnostics")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if _, ok := a.validateRepo(*repoFlag); !ok {
		return 2
	}
	plan, err := workflow.ResolveWithOptions(workflow.ResolveOptions{Root: ".", Schema: *schema})
	result := workflowCommandResult{
		OK:       err == nil && !plan.HasErrors(),
		Repo:     *repoFlag,
		Workflow: plan,
	}
	if err != nil {
		result.Error = err.Error()
	}
	if *jsonOut {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
		if !result.OK {
			return 1
		}
		return 0
	}
	if which {
		printWorkflowWhich(a.out, result)
	} else {
		printWorkflowValidate(a.out, result)
	}
	if !result.OK {
		return 1
	}
	return 0
}

func printWorkflowValidate(out interface{ Write([]byte) (int, error) }, result workflowCommandResult) {
	if result.OK {
		fmt.Fprintln(out, "workflow validation OK")
	} else {
		fmt.Fprintln(out, "workflow validation failed")
	}
	printWorkflowSummary(out, result.Workflow)
	printWorkflowDiagnostics(out, result.Workflow.Diagnostics)
	if result.Error != "" && !strings.Contains(result.Error, "workflow validation failed") {
		fmt.Fprintf(out, "error: %s\n", result.Error)
	}
}

func printWorkflowWhich(out interface{ Write([]byte) (int, error) }, result workflowCommandResult) {
	printWorkflowSummary(out, result.Workflow)
	printWorkflowDiagnostics(out, result.Workflow.Diagnostics)
}

func printWorkflowSummary(out interface{ Write([]byte) (int, error) }, plan workflow.Plan) {
	fmt.Fprintf(out, "workflow source: %s\n", plan.Source.Kind)
	fmt.Fprintf(out, "schema: %s\n", plan.Source.SchemaName)
	if plan.Source.ConfigPath != "" {
		fmt.Fprintf(out, "config: %s\n", plan.Source.ConfigPath)
	}
	if plan.Source.SchemaPath != "" {
		fmt.Fprintf(out, "schema path: %s\n", plan.Source.SchemaPath)
	}
	if plan.Source.TemplateDir != "" {
		fmt.Fprintf(out, "template dir: %s\n", plan.Source.TemplateDir)
	}
	if len(plan.Artifacts) > 0 {
		fmt.Fprintln(out, "artifacts:")
		for _, artifact := range plan.Artifacts {
			fmt.Fprintf(out, "- %s type=%s", artifact.ID, artifact.Type)
			if artifact.Template != "" {
				fmt.Fprintf(out, " template=%s", artifact.Template)
			}
			if len(artifact.Storage) > 0 {
				fmt.Fprintf(out, " storage=%s", strings.Join(artifact.Storage, ","))
			}
			fmt.Fprintln(out)
		}
	}
}

func printWorkflowDiagnostics(out interface{ Write([]byte) (int, error) }, diagnostics []workflow.Diagnostic) {
	if len(diagnostics) == 0 {
		return
	}
	fmt.Fprintln(out, "diagnostics:")
	for _, diagnostic := range diagnostics {
		target := diagnostic.Path
		if diagnostic.Artifact != "" {
			if target != "" {
				target = diagnostic.Artifact + " " + target
			} else {
				target = diagnostic.Artifact
			}
		}
		if target != "" {
			fmt.Fprintf(out, "- %s %s %s: %s\n", diagnostic.Severity, diagnostic.Code, target, diagnostic.Message)
		} else {
			fmt.Fprintf(out, "- %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
}
