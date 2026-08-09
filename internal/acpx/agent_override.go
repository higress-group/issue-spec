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
	"sync"
	"time"
)

const maxAgentConfigBytes = 1 << 20

// overrideLockTimeout bounds how long ApplyAgentOverride waits for the
// cross-process config lock before failing the dispatch with a clear error.
// The lock is only ever held for one read-modify-write of a <=1MiB file, so
// two seconds is generous.
const overrideLockTimeout = 2 * time.Second

var codexAdapterPattern = regexp.MustCompile(`@agentclientprotocol/codex-acp@[A-Za-z0-9._~^+\-]+`)

// overrideWriteLocks serializes in-process writers per config file. The flock
// on config.json.lock additionally serializes cooperating processes (e.g. a
// runner and an operator CLI sharing one HOME). Entries persist for the
// process lifetime: one per distinct lock path, bounded in practice by the
// dispatcher's stable runner homes. Ad-hoc callers on the MkdirTemp fallback
// path can accrue one entry per call; accepted, since growth stays
// process-lifetime bounded.
var overrideWriteLocks sync.Map // map[string]*sync.Mutex, keyed by cleaned lock path

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
	if err := validateOverrideCommand(agent, command, selected.Args); err != nil {
		return AgentOverride{}, false, err
	}
	args := make([]string, len(selected.Args))
	copy(args, selected.Args)
	return AgentOverride{Agent: agent, Command: command, Args: args, Source: path}, true, nil
}

// validateOverrideCommand enforces the exact bounds LoadAgentOverride enforces
// when reading an override back, so a value written through ApplyAgentOverride
// can never be rejected by the reader.
func validateOverrideCommand(agent, command string, args []string) error {
	if len(command) > 4096 || strings.ContainsAny(command, "\x00\r\n") {
		return fmt.Errorf("acpx %s agent override is invalid", agent)
	}
	if len(args) > 64 {
		return fmt.Errorf("acpx %s agent override has too many arguments", agent)
	}
	total := 0
	for _, arg := range args {
		total += len(arg)
		if len(arg) > 4096 || total > 16*1024 || strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("acpx %s agent override arguments are invalid", agent)
		}
	}
	return nil
}

// ApplyAgentOverride merges one agent's override into
// <home>/.acpx/config.json under an in-process keyed mutex plus a bounded
// flock on config.json.lock, so concurrent dispatches sharing one runtime
// HOME never clobber each other's per-agent entries. A nil override deletes
// only that agent's entry; when no agents and no other top-level keys remain
// the file existed solely for overrides and is removed. Every other top-level
// field and every other agent's raw entry is preserved. The lock is held only
// for this single read-modify-write, and the result is installed with a temp
// file plus rename, so readers never observe a partial document. A malformed,
// oversized, symlinked, or otherwise non-regular config fails closed: the
// error is returned and the file is left untouched.
func ApplyAgentOverride(home, agent string, override *AgentOverride) error {
	agent = strings.TrimSpace(agent)
	if strings.TrimSpace(home) == "" || agent == "" {
		return fmt.Errorf("acpx agent override destination and agent are required")
	}
	dir := filepath.Join(filepath.Clean(home), ".acpx")
	target := filepath.Join(dir, "config.json")
	if override != nil {
		if name := strings.TrimSpace(override.Agent); name != "" && name != agent {
			return fmt.Errorf("acpx %s agent override does not match agent %q", name, agent)
		}
		if strings.TrimSpace(override.Command) == "" {
			return fmt.Errorf("acpx %s agent override command is required", agent)
		}
		if err := validateOverrideCommand(agent, strings.TrimSpace(override.Command), override.Args); err != nil {
			return err
		}
	} else if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		// Delete against a missing file is a no-op; skip creating the
		// directory and lock file for it.
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect acpx config: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lockPath := target + ".lock"
	guard := overrideWriteLock(lockPath)
	guard.Lock()
	defer guard.Unlock()
	lockFile, err := acquireOverrideFileLock(lockPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlockOverrideFile(lockFile)
		_ = lockFile.Close()
	}()

	doc := map[string]json.RawMessage{}
	agents := map[string]json.RawMessage{}
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Fresh document; a delete below is a no-op.
	case err != nil:
		return fmt.Errorf("inspect acpx config: %w", err)
	default:
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxAgentConfigBytes {
			return fmt.Errorf("acpx config must be a bounded regular file")
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("read acpx config: %w", err)
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse acpx config: %w", err)
		}
		if doc == nil { // top-level JSON null
			doc = map[string]json.RawMessage{}
		}
		if raw, ok := doc["agents"]; ok {
			if err := json.Unmarshal(raw, &agents); err != nil {
				return fmt.Errorf("parse acpx agents: %w", err)
			}
			if agents == nil { // "agents": null
				agents = map[string]json.RawMessage{}
			}
		}
	}

	if override == nil {
		if _, present := agents[agent]; !present {
			return nil
		}
		delete(agents, agent)
		if len(agents) == 0 && len(doc) == 1 {
			// The file existed solely for overrides; remove it whole rather
			// than leaving an empty agents shell behind.
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove acpx config: %w", err)
			}
			return nil
		}
	} else {
		entry, err := json.Marshal(struct {
			Command string   `json:"command"`
			Args    []string `json:"args,omitempty"`
		}{Command: strings.TrimSpace(override.Command), Args: append([]string(nil), override.Args...)})
		if err != nil {
			return err
		}
		agents[agent] = entry
	}
	rawAgents, err := json.Marshal(agents)
	if err != nil {
		return err
	}
	doc["agents"] = rawAgents
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeAgentConfigAtomic(dir, target, append(data, '\n'))
}

func overrideWriteLock(lockPath string) *sync.Mutex {
	guard, _ := overrideWriteLocks.LoadOrStore(lockPath, &sync.Mutex{})
	return guard.(*sync.Mutex)
}

func acquireOverrideFileLock(lockPath string) (*os.File, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open acpx config lock: %w", err)
	}
	deadline := time.Now().Add(overrideLockTimeout)
	delay := 5 * time.Millisecond
	for {
		err := tryLockFile(file)
		if err == nil {
			return file, nil
		}
		if !lockUnavailable(err) {
			_ = file.Close()
			return nil, fmt.Errorf("lock acpx config: %w", err)
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("timed out after %s waiting for the acpx config lock", overrideLockTimeout)
		}
		time.Sleep(delay)
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

// writeAgentConfigAtomic installs data at target through a temp file in the
// same directory plus rename, so a concurrent reader never observes partial
// JSON. A crash between temp creation and rename strands at most one bounded
// `.config-*` temp file per crashed apply; the next apply self-heals.
func writeAgentConfigAtomic(dir, target string, data []byte) error {
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
	if _, err := temporary.Write(data); err != nil {
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
	return os.Rename(name, target)
}

// MaterializeAgentOverride installs one agent's override, preserving other
// agents' entries and unrelated top-level fields already present in the
// config. It is ApplyAgentOverride with an upsert; no caller can whole-file
// clobber a shared config through it.
func MaterializeAgentOverride(home string, override AgentOverride) error {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(override.Agent) == "" || strings.TrimSpace(override.Command) == "" {
		return fmt.Errorf("acpx agent override destination and value are required")
	}
	return ApplyAgentOverride(home, override.Agent, &override)
}

func AgentOverrideDescription(override AgentOverride) string {
	full := strings.Join(append([]string{override.Command}, override.Args...), " ")
	if adapter := codexAdapterPattern.FindString(full); adapter != "" {
		return adapter
	}
	sum := sha256.Sum256([]byte(full))
	return "custom-command-sha256:" + hex.EncodeToString(sum[:6])
}
