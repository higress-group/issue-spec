package acpx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentOverrideSelectivelyLoadsAndMaterializesOneCommand(t *testing.T) {
	host, runtime := t.TempDir(), t.TempDir()
	source := `{"defaultAgent":"claude","agents":{"codex":{"command":"npx","args":["-y","@agentclientprotocol/codex-acp@1.1.2"]},"claude":{"command":"secret-command"}},"authMethods":[{"token":"must-not-cross"}]}`
	if err := os.MkdirAll(filepath.Join(host, ".acpx"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(host, ".acpx", "config.json"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	override, ok, err := LoadAgentOverride(host, AgentCodex)
	if err != nil || !ok {
		t.Fatalf("LoadAgentOverride = %+v/%v/%v", override, ok, err)
	}
	if got := AgentOverrideDescription(override); got != "@agentclientprotocol/codex-acp@1.1.2" {
		t.Fatalf("description = %q", got)
	}
	if err := MaterializeAgentOverride(runtime, override); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runtime, ".acpx", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "codex-acp@1.1.2") || !strings.Contains(string(data), `"args"`) || strings.Contains(string(data), "must-not-cross") || strings.Contains(string(data), "secret-command") {
		t.Fatalf("materialized config = %s", data)
	}
	info, err := os.Stat(filepath.Join(runtime, ".acpx", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("materialized mode = %v", info.Mode().Perm())
	}
}

func TestAgentOverrideRejectsUnboundedOrMultilineCommands(t *testing.T) {
	for _, command := range []string{"line one\nline two", strings.Repeat("x", 4097)} {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".acpx"), 0o700); err != nil {
			t.Fatal(err)
		}
		body := `{"agents":{"codex":{"command":` + string(mustJSON(t, command)) + `}}}`
		if err := os.WriteFile(filepath.Join(home, ".acpx", "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadAgentOverride(home, AgentCodex); err == nil {
			t.Fatalf("unsafe command accepted: %q", command[:min(len(command), 32)])
		}
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
