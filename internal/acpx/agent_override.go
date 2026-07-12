package acpx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxAgentConfigBytes = 1 << 20

var codexAdapterPattern = regexp.MustCompile(`@agentclientprotocol/codex-acp@[A-Za-z0-9._~^+\-]+`)

type AgentOverride struct {
	Agent   string
	Command string
	Args    []string
	Source  string
}

func LoadAgentOverride(home, agent string) (AgentOverride, bool, error) {
	agent = strings.TrimSpace(agent)
	if strings.TrimSpace(home) == "" || agent == "" {
		return AgentOverride{}, false, nil
	}
	path := filepath.Join(filepath.Clean(home), ".acpx", "config.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return AgentOverride{}, false, nil
	}
	if err != nil {
		return AgentOverride{}, false, fmt.Errorf("inspect acpx config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxAgentConfigBytes {
		return AgentOverride{}, false, fmt.Errorf("acpx config must be a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentOverride{}, false, fmt.Errorf("read acpx config: %w", err)
	}
	var source struct {
		Agents map[string]json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return AgentOverride{}, false, fmt.Errorf("parse acpx config: %w", err)
	}
	raw, ok := source.Agents[agent]
	if !ok {
		return AgentOverride{}, false, nil
	}
	var selected struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &selected); err != nil {
		return AgentOverride{}, false, fmt.Errorf("parse acpx %s agent override: %w", agent, err)
	}
	command := strings.TrimSpace(selected.Command)
	if command == "" {
		return AgentOverride{}, false, nil
	}
	if len(command) > 4096 || strings.ContainsAny(command, "\x00\r\n") {
		return AgentOverride{}, false, fmt.Errorf("acpx %s agent override is invalid", agent)
	}
	if len(selected.Args) > 64 {
		return AgentOverride{}, false, fmt.Errorf("acpx %s agent override has too many arguments", agent)
	}
	args := make([]string, len(selected.Args))
	total := 0
	for index, arg := range selected.Args {
		total += len(arg)
		if len(arg) > 4096 || total > 16*1024 || strings.ContainsAny(arg, "\x00\r\n") {
			return AgentOverride{}, false, fmt.Errorf("acpx %s agent override arguments are invalid", agent)
		}
		args[index] = arg
	}
	return AgentOverride{Agent: agent, Command: command, Args: args, Source: path}, true, nil
}

func MaterializeAgentOverride(home string, override AgentOverride) error {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(override.Agent) == "" || strings.TrimSpace(override.Command) == "" {
		return fmt.Errorf("acpx agent override destination and value are required")
	}
	dir := filepath.Join(filepath.Clean(home), ".acpx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	payload := struct {
		Agents map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args,omitempty"`
		} `json:"agents"`
	}{Agents: map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args,omitempty"`
	}{override.Agent: {Command: override.Command, Args: append([]string(nil), override.Args...)}}}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	target := filepath.Join(dir, "config.json")
	if info, err := os.Lstat(target); err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(name, target)
}

func AgentOverrideDescription(override AgentOverride) string {
	full := strings.Join(append([]string{override.Command}, override.Args...), " ")
	if adapter := codexAdapterPattern.FindString(full); adapter != "" {
		return adapter
	}
	sum := sha256.Sum256([]byte(full))
	return "custom-command-sha256:" + hex.EncodeToString(sum[:6])
}
