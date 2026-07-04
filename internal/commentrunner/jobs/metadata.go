package jobs

import (
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// BoundedACPXKeys defines the allowlist for ACPX metadata in state.
// Maps various possible raw key names to canonical names for storage.
var BoundedACPXKeys = map[string]string{
	// Stable identifiers
	"acpxRecordId":       "stable_record_id",
	"acpx_record_id":     "stable_record_id",
	"recordId":           "stable_record_id",
	"record_id":          "stable_record_id",
	"id":                 "stable_record_id",
	"stableRecordId":     "stable_record_id",
	"stable_record_id":   "stable_record_id",

	// Session identification
	"acpxSessionId":      "true_session_id",
	"acpx_session_id":    "true_session_id",
	"sessionId":          "true_session_id",
	"session_id":         "true_session_id",
	"trueSessionId":      "true_session_id",
	"true_session_id":    "true_session_id",

	// Provider session tracking
	"agentSessionId":     "provider_session_id",
	"agent_session_id":   "provider_session_id",
	"providerSessionId":  "provider_session_id",
	"provider_session_id": "provider_session_id",

	// Turn tracking
	"lastTurnId":         "last_turn_id",
	"last_turn_id":       "last_turn_id",
	"turnId":             "last_turn_id",
	"turn_id":            "last_turn_id",
	"lastPromptId":       "last_turn_id",
	"last_prompt_id":     "last_turn_id",

	// Agent info
	"agent":              "agent",
	"agentCommand":       "agent",
	"agent_command":      "agent",
	"provider":           "agent",
	"model":              "model",

	// Session naming
	"name":               "session_name",
	"sessionName":        "session_name",
	"session_name":       "session_name",

	// Working directory (for legacy resume compatibility)
	"cwd":                "cwd",
	"workingDirectory":   "cwd",
	"working_directory":  "cwd",

	// History length (for diagnostics, not the history itself)
	"historyLength":      "history_length",
	"history_length":     "history_length",
	"turnCount":          "history_length",
	"turn_count":         "history_length",

	// Mode (when required for resume compatibility)
	"acpx.desired_mode_id":  "mode",
	"acpx.current_mode_id":  "mode",
	"desiredMode":           "mode",
	"desired_mode":          "mode",
	"mode":                  "mode",
}

// BoundedAcpxMetadata creates a bounded ACPX metadata for state storage.
// It filters out unbounded content like message history, tool results,
// and command outputs, keeping only control-plane metadata.
func BoundedAcpxMetadata(meta acpx.Metadata, at time.Time) state.AcpxMetadata {
	refreshed := meta.RefreshedAt
	if refreshed.IsZero() {
		refreshed = at
	}

	// Build bounded raw map
	boundedRaw := make(map[string]string)
	for key, value := range meta.Raw {
		// Skip keys that look like message history
		if isMessageKey(key) {
			continue
		}
		// Skip keys that look like tool results
		if isToolResultKey(key) {
			continue
		}
		// Skip keys that look like command content
		if isCommandContentKey(key) {
			continue
		}
		// Skip keys that look like large outputs
		if isOutputKey(key) {
			continue
		}

		// Map to canonical name if in allowlist
		if canonicalName, ok := BoundedACPXKeys[key]; ok {
			// Skip if we already have a value for this canonical name
			if _, exists := boundedRaw[canonicalName]; !exists {
				boundedRaw[canonicalName] = value
			}
		}
	}

	return state.AcpxMetadata{
		StableRecordID:    meta.StableRecordID,
		TrueSessionID:     meta.TrueSessionID,
		ProviderSessionID: meta.ProviderSessionID,
		LastTurnID:        meta.LastTurnID,
		RefreshedAt:       refreshed,
		Raw:               boundedRaw,
	}
}

// isMessageKey checks if a key is part of message history.
func isMessageKey(key string) bool {
	keyPrefixes := []string{
		"messages.", "messages[",
		"history.", "history[",
		"turn.", "turn[",
		"prompt.", "prompt[",
	}
	for _, prefix := range keyPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// isToolResultKey checks if a key is part of tool results.
func isToolResultKey(key string) bool {
	keyPrefixes := []string{
		"tool_results.", "tool_results[",
		"toolResults.", "toolResults[",
		"tool.", "tool[",
		"function_results.", "function_results[",
	}
	for _, prefix := range keyPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// isCommandContentKey checks if a key contains command text or input.
func isCommandContentKey(key string) bool {
	deniedKeys := []string{
		"command", "commands", "commandText", "command_text",
		"prompt", "prompts", "input", "inputs",
		"user_input", "userInput",
	}
	for _, denied := range deniedKeys {
		if key == denied {
			return true
		}
	}
	return false
}

// isOutputKey checks if a key contains stdout/stderr content or results.
func isOutputKey(key string) bool {
	deniedKeys := []string{
		"stdout", "stderr", "output", "outputs",
		"result", "results", "response", "responses",
		"error_output", "errorOutput",
		"completion", "text",
	}
	deniedSuffixes := []string{
		".stdout", ".stderr", ".output", ".result",
		"_stdout", "_stderr", "_output", "_result",
	}
	for _, denied := range deniedKeys {
		if key == denied {
			return true
		}
	}
	for _, suffix := range deniedSuffixes {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}
