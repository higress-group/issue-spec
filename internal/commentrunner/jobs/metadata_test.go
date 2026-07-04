package jobs

import (
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestBoundedAcpxMetadata(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name     string
		input    acpx.Metadata
		expected state.AcpxMetadata
	}{
		{
			name: "filters out message history",
			input: acpx.Metadata{
				StableRecordID:    "rec-123",
				TrueSessionID:     "ses-456",
				ProviderSessionID: "prov-789",
				LastTurnID:        "turn-012",
				RefreshedAt:       now,
				Raw: map[string]string{
					"stable_record_id":            "rec-123",
					"messages.0.content":          "some message content",
					"messages.0.role":             "user",
					"tool_results.0.output":       "tool output",
					"command":                     "run command",
					"stdout":                      "some output",
					"historyLength":               "10",
				},
			},
			expected: state.AcpxMetadata{
				StableRecordID:    "rec-123",
				TrueSessionID:     "ses-456",
				ProviderSessionID: "prov-789",
				LastTurnID:        "turn-012",
				RefreshedAt:       now,
				Raw: map[string]string{
					"stable_record_id": "rec-123",
					"history_length":   "10",
				},
			},
		},
		{
			name: "preserves cwd for compatibility",
			input: acpx.Metadata{
				StableRecordID: "rec-123",
				RefreshedAt:    now,
				Raw: map[string]string{
					"cwd": "/path/to/workspace",
				},
			},
			expected: state.AcpxMetadata{
				StableRecordID: "rec-123",
				RefreshedAt:    now,
				Raw: map[string]string{
					"cwd": "/path/to/workspace",
				},
			},
		},
		{
			name: "maps variant keys to canonical names",
			input: acpx.Metadata{
				StableRecordID:    "rec-123",
				TrueSessionID:     "ses-456",
				ProviderSessionID: "prov-789",
				LastTurnID:        "turn-012",
				RefreshedAt:       now,
				Raw: map[string]string{
					"acpx_record_id":    "rec-123",
					"true_session_id":    "ses-456",
					"agentSessionId":     "prov-789",
					"lastPromptId":       "turn-012",
					"agent":              "claude",
				},
			},
			expected: state.AcpxMetadata{
				StableRecordID:    "rec-123",
				TrueSessionID:     "ses-456",
				ProviderSessionID: "prov-789",
				LastTurnID:        "turn-012",
				RefreshedAt:       now,
				Raw: map[string]string{
					"stable_record_id":   "rec-123",
					"true_session_id":    "ses-456",
					"provider_session_id": "prov-789",
					"last_turn_id":       "turn-012",
					"agent":              "claude",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BoundedAcpxMetadata(tt.input, now)

			if result.StableRecordID != tt.expected.StableRecordID {
				t.Errorf("StableRecordID = %v, want %v", result.StableRecordID, tt.expected.StableRecordID)
			}
			if result.TrueSessionID != tt.expected.TrueSessionID {
				t.Errorf("TrueSessionID = %v, want %v", result.TrueSessionID, tt.expected.TrueSessionID)
			}
			if result.ProviderSessionID != tt.expected.ProviderSessionID {
				t.Errorf("ProviderSessionID = %v, want %v", result.ProviderSessionID, tt.expected.ProviderSessionID)
			}
			if result.LastTurnID != tt.expected.LastTurnID {
				t.Errorf("LastTurnID = %v, want %v", result.LastTurnID, tt.expected.LastTurnID)
			}

			// Check Raw fields
			for k, v := range tt.expected.Raw {
				if result.Raw[k] != v {
					t.Errorf("Raw[%s] = %v, want %v", k, result.Raw[k], v)
				}
			}
			// Ensure no unexpected fields
			for k := range result.Raw {
				if _, ok := tt.expected.Raw[k]; !ok {
					t.Errorf("Unexpected Raw field: %s", k)
				}
			}
		})
	}
}

func TestIsMessageKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"messages.0.content", true},
		{"messages[0].content", true},
		{"history.0.text", true},
		{"turn.1.output", true},
		{"prompt.0", true},
		{"stable_record_id", false},
		{"cwd", false},
		{"agent", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			result := isMessageKey(tt.key)
			if result != tt.expected {
				t.Errorf("isMessageKey(%q) = %v, want %v", tt.key, result, tt.expected)
			}
		})
	}
}

func TestIsToolResultKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"tool_results.0.output", true},
		{"toolResults.0.output", true},
		{"tool.0.name", true},
		{"function_results.0", true},
		{"stable_record_id", false},
		{"cwd", false},
		{"agent", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			result := isToolResultKey(tt.key)
			if result != tt.expected {
				t.Errorf("isToolResultKey(%q) = %v, want %v", tt.key, result, tt.expected)
			}
		})
	}
}

func TestIsCommandContentKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"command", true},
		{"commands", true},
		{"commandText", true},
		{"prompt", true},
		{"input", true},
		{"user_input", true},
		{"stable_record_id", false},
		{"cwd", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			result := isCommandContentKey(tt.key)
			if result != tt.expected {
				t.Errorf("isCommandContentKey(%q) = %v, want %v", tt.key, result, tt.expected)
			}
		})
	}
}

func TestIsOutputKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"stdout", true},
		{"stderr", true},
		{"output", true},
		{"result", true},
		{"error.stdout", true},
		{"response_stderr", true},
		{"stable_record_id", false},
		{"cwd", false},
		{"agent", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			result := isOutputKey(tt.key)
			if result != tt.expected {
				t.Errorf("isOutputKey(%q) = %v, want %v", tt.key, result, tt.expected)
			}
		})
	}
}

func TestBoundedAcpxMetadataZeroRefreshedAt(t *testing.T) {
	now := time.Now().UTC()
	meta := acpx.Metadata{
		StableRecordID: "rec-123",
		Raw: map[string]string{
			"cwd": "/path",
		},
	}

	result := BoundedAcpxMetadata(meta, now)

	if result.RefreshedAt.IsZero() {
		t.Error("RefreshedAt should not be zero when input is zero")
	}
	if !result.RefreshedAt.Equal(now) {
		t.Errorf("RefreshedAt = %v, want %v", result.RefreshedAt, now)
	}
}
