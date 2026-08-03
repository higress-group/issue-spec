package commands

import (
	"context"
	"encoding/json"
	"strings"
)

const deprecatedWorkflowCode = "deprecated_workflow"

type deprecatedWorkflowResult struct {
	OK          bool   `json:"ok"`
	Code        string `json:"code"`
	Command     string `json:"command"`
	Message     string `json:"message"`
	Replacement string `json:"replacement,omitempty"`
}

// deprecatedWorkflowSelection recognizes only retired command writers and the
// retired final-evidence read gate. It runs before command-specific flag,
// filesystem, authentication, backend, evidence, relationship, or provider
// handling, so these surfaces have one mechanically zero-write boundary.
func deprecatedWorkflowSelection(args []string) (deprecatedWorkflowResult, bool) {
	if len(args) == 0 {
		return deprecatedWorkflowResult{}, false
	}
	command, replacement := "", ""
	switch args[0] {
	case "review":
		command, replacement = "review", "provider-native review"
		if len(args) > 1 {
			command += " " + args[1]
		}
	case "verify":
		command, replacement = "verify", "project tests and human review handoff"
		if len(args) > 1 && args[1] == "submit" {
			command = "verify submit"
		}
	case "pr":
		if len(args) > 1 && (args[1] == "rationale" || args[1] == "verify-closure") {
			command = "pr " + args[1]
			replacement = "provider-native discussion"
			if args[1] == "verify-closure" {
				replacement = "provider-native closing or manual issue close"
			}
		}
	case "code-change":
		if len(args) > 1 && args[1] == "rationale" {
			command, replacement = "code-change rationale", "provider-native discussion"
		}
	case "finalize":
		command, replacement = "finalize", "human review handoff"
		if len(args) > 1 {
			command += " " + args[1]
		}
	case "archive":
		command, replacement = "archive", "durable-spec preview|apply|check|detail"
		if len(args) > 1 {
			command += " " + args[1]
		}
	case "issue":
		if len(args) > 1 && args[1] == "close-change" {
			command, replacement = "issue close-change", "provider-native closing or manual issue close"
		}
	case "status":
		if selectedFinalStatusGate(args[1:]) {
			command, replacement = "status --gate final", "project tests and human review handoff"
		}
	}
	if command == "" {
		return deprecatedWorkflowResult{}, false
	}
	return deprecatedWorkflowResult{OK: false, Code: deprecatedWorkflowCode, Command: command,
		Message:     command + " belongs to the retired final-evidence workflow and performs no mutation",
		Replacement: replacement}, true
}

func (a *app) runReview(_ context.Context, args []string) int {
	return a.outputDeprecatedWorkflow(deprecatedResultFor("review", args, "provider-native review"), append([]string{"review"}, args...))
}

func (a *app) runVerify(_ context.Context, args []string) int {
	return a.outputDeprecatedWorkflow(deprecatedResultFor("verify", args, "project tests and human review handoff"), append([]string{"verify"}, args...))
}

func (a *app) runArchive(_ context.Context, args []string) int {
	return a.outputDeprecatedWorkflow(deprecatedResultFor("archive", args, "durable-spec preview|apply|check|detail"), append([]string{"archive"}, args...))
}

func (a *app) runIssueCloseChange(_ context.Context, args []string) int {
	return a.outputDeprecatedWorkflow(deprecatedResultFor("issue close-change", args, "provider-native closing or manual issue close"), append([]string{"issue", "close-change"}, args...))
}

func deprecatedResultFor(command string, args []string, replacement string) deprecatedWorkflowResult {
	if len(args) > 0 {
		command += " " + args[0]
	}
	return deprecatedWorkflowResult{OK: false, Code: deprecatedWorkflowCode, Command: command,
		Message: command + " belongs to the retired final-evidence workflow and performs no mutation", Replacement: replacement}
}

func selectedFinalStatusGate(args []string) bool {
	for index := 0; index < len(args); index++ {
		switch {
		case args[index] == "--gate" && index+1 < len(args):
			return strings.EqualFold(strings.TrimSpace(args[index+1]), "final")
		case strings.HasPrefix(args[index], "--gate="):
			return strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(args[index], "--gate=")), "final")
		}
	}
	return false
}

func (a *app) outputDeprecatedWorkflow(result deprecatedWorkflowResult, args []string) int {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
			break
		}
	}
	if jsonOutput {
		_ = a.outputJSON(result)
		return 1
	}
	payload, err := json.Marshal(result)
	if err != nil {
		a.errorf("%s: %s\n", deprecatedWorkflowCode, result.Message)
		return 1
	}
	a.errorf("%s\n", payload)
	return 1
}
