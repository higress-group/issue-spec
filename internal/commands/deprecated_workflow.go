package commands

import (
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
		if len(args) > 1 && (args[1] == "submit" || args[1] == "sync") {
			command, replacement = "review "+args[1], "provider review decisions or configured review decide fallback"
		}
	case "verify":
		command, replacement = "verify", "merge-check"
		if len(args) > 1 && args[1] == "submit" {
			command = "verify submit"
		}
	case "pr":
		if len(args) > 1 && args[1] == "rationale" {
			command, replacement = "pr rationale", "provider-native discussion"
		}
	case "code-change":
		if len(args) > 1 && args[1] == "rationale" {
			command, replacement = "code-change rationale", "provider-native discussion"
		}
	case "finalize":
		if len(args) > 1 && (args[1] == "preview" || args[1] == "apply" || args[1] == "detail") {
			command, replacement = "finalize "+args[1], "code-change merge"
		}
	case "archive":
		if len(args) > 1 && args[1] == "durable-spec" {
			command, replacement = "archive durable-spec", "durable-spec preview|apply|check|detail"
		}
	case "status":
		if selectedFinalStatusGate(args[1:]) {
			command, replacement = "status --gate final", "merge-check"
		}
	}
	if command == "" {
		return deprecatedWorkflowResult{}, false
	}
	return deprecatedWorkflowResult{OK: false, Code: deprecatedWorkflowCode, Command: command,
		Message:     command + " belongs to the retired final-evidence workflow and performs no mutation",
		Replacement: replacement}, true
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
