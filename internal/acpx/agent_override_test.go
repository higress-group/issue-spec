package acpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// Regression for the shared-HOME race: one dispatch materializes its agent's
// host override while a peer dispatch for another agent has NO host override
// and deletes its (absent) entry. The peer's delete must never erase the
// materialized entry. At HEAD both dispatches ran on the whole file
// (remove / single-agent replace) and the override was lost.
func TestApplyAgentOverrideConcurrentUpsertAndPeerDeleteKeepsEntry(t *testing.T) {
	home := t.TempDir()
	qoder := AgentOverride{Agent: AgentQoder, Command: "npx", Args: []string{"-y", "@qodercode/acp@1.2.3"}}
	const rounds = 50
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := ApplyAgentOverride(home, AgentQoder, &qoder); err != nil {
				t.Errorf("upsert qoder: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := ApplyAgentOverride(home, AgentCodex, nil); err != nil {
				t.Errorf("delete codex: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	got, ok, err := LoadAgentOverride(home, AgentQoder)
	if err != nil || !ok {
		t.Fatalf("qoder override lost under concurrent peer delete: ok=%v err=%v", ok, err)
	}
	if got.Command != qoder.Command || strings.Join(got.Args, "\x00") != strings.Join(qoder.Args, "\x00") {
		t.Fatalf("qoder override = %+v, want %+v", got, qoder)
	}
	if _, ok, err := LoadAgentOverride(home, AgentCodex); err != nil || ok {
		t.Fatalf("codex entry should stay absent: ok=%v err=%v", ok, err)
	}
}

// Regression for the whole-file last-writer-wins race: two dispatches with
// different host overrides must both end up in the shared config.
func TestApplyAgentOverrideConcurrentDistinctUpsertsKeepBothEntries(t *testing.T) {
	home := t.TempDir()
	qoder := AgentOverride{Agent: AgentQoder, Command: "npx", Args: []string{"-y", "@qodercode/acp@1.2.3"}}
	codex := AgentOverride{Agent: AgentCodex, Command: "npx", Args: []string{"-y", "@agentclientprotocol/codex-acp@1.1.2"}}
	const rounds = 50
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := ApplyAgentOverride(home, AgentQoder, &qoder); err != nil {
				t.Errorf("upsert qoder: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := ApplyAgentOverride(home, AgentCodex, &codex); err != nil {
				t.Errorf("upsert codex: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	for _, want := range []AgentOverride{qoder, codex} {
		got, ok, err := LoadAgentOverride(home, want.Agent)
		if err != nil || !ok {
			t.Fatalf("%s override lost under concurrent upserts: ok=%v err=%v", want.Agent, ok, err)
		}
		if got.Command != want.Command || strings.Join(got.Args, "\x00") != strings.Join(want.Args, "\x00") {
			t.Fatalf("%s override = %+v, want %+v", want.Agent, got, want)
		}
	}
}

// Stress: interleaved upsert qoder / upsert codex / delete claude on one home
// while concurrent readers hammer LoadAgentOverride. Readers must never
// observe a partial document, and the final file must contain exactly
// qoder+codex. Must pass under -race.
func TestApplyAgentOverrideConcurrentStress(t *testing.T) {
	home := t.TempDir()
	seedConfig(t, home, `{"defaultAgent":"claude","agents":{"claude":{"command":"claude-acp","args":["--serve"]}}}`)
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp", Args: []string{"--stdio"}}
	codex := AgentOverride{Agent: AgentCodex, Command: "codex-acp"}
	const rounds = 40
	var stop atomic.Bool
	var readers sync.WaitGroup
	for _, agent := range []string{AgentQoder, AgentCodex, AgentClaude} {
		readers.Add(1)
		go func(agent string) {
			defer readers.Done()
			for !stop.Load() {
				if _, _, err := LoadAgentOverride(home, agent); err != nil {
					t.Errorf("reader(%s) observed a broken config: %v", agent, err)
					return
				}
			}
		}(agent)
	}
	var writers sync.WaitGroup
	writers.Add(3)
	go func() {
		defer writers.Done()
		for i := 0; i < rounds; i++ {
			if err := ApplyAgentOverride(home, AgentQoder, &qoder); err != nil {
				t.Errorf("upsert qoder: %v", err)
				return
			}
		}
	}()
	go func() {
		defer writers.Done()
		for i := 0; i < rounds; i++ {
			if err := ApplyAgentOverride(home, AgentCodex, &codex); err != nil {
				t.Errorf("upsert codex: %v", err)
				return
			}
		}
	}()
	go func() {
		defer writers.Done()
		for i := 0; i < rounds; i++ {
			if err := ApplyAgentOverride(home, AgentClaude, nil); err != nil {
				t.Errorf("delete claude: %v", err)
				return
			}
		}
	}()
	writers.Wait()
	stop.Store(true)
	readers.Wait()

	data, err := os.ReadFile(configPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Agents map[string]json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("final config is not valid JSON: %v", err)
	}
	if len(doc.Agents) != 2 {
		t.Fatalf("final agents = %v, want exactly qoder+codex", agentNames(doc.Agents))
	}
	for _, agent := range []string{AgentQoder, AgentCodex} {
		if _, ok := doc.Agents[agent]; !ok {
			t.Fatalf("final config missing %s entry: %s", agent, data)
		}
	}
	info, err := os.Stat(configPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("final config mode = %v, want 0600", info.Mode().Perm())
	}
}

// Unrelated top-level fields and other agents' raw entries must survive an
// upsert byte-for-byte (modulo JSON-insignificant whitespace).
func TestApplyAgentOverridePreservesUnrelatedFieldsAndPeerAgents(t *testing.T) {
	home := t.TempDir()
	other := `{"command":"keep-me","args":["--x"],"extra":{"nested":[1,2,{"a":"b"}]}}`
	seedConfig(t, home, `{"defaultAgent":"claude","ttl":30,"custom":{"nested":[1,2,{"a":"b"}]},"agents":{"other":`+other+`}}`)
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp", Args: []string{"--stdio"}}
	if err := ApplyAgentOverride(home, AgentQoder, &qoder); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	assertRawJSONEqual(t, doc["defaultAgent"], `"claude"`)
	assertRawJSONEqual(t, doc["ttl"], `30`)
	assertRawJSONEqual(t, doc["custom"], `{"nested":[1,2,{"a":"b"}]}`)
	var agents map[string]json.RawMessage
	if err := json.Unmarshal(doc["agents"], &agents); err != nil {
		t.Fatal(err)
	}
	assertRawJSONEqual(t, agents["other"], other)
	assertRawJSONEqual(t, agents[AgentQoder], `{"command":"qoder-acp","args":["--stdio"]}`)
}

// A delete that empties agents removes the file only when it existed solely
// for overrides; any other top-level key keeps the file (minus the entry).
func TestApplyAgentOverrideDeleteRemovesAgentsOnlyFile(t *testing.T) {
	home := t.TempDir()
	seedConfig(t, home, `{"agents":{"claude":{"command":"claude-acp"}}}`)
	if err := ApplyAgentOverride(home, AgentClaude, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(configPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agents-only config should have been removed: %v", err)
	}
}

func TestApplyAgentOverrideDeleteKeepsFileWithOtherContent(t *testing.T) {
	home := t.TempDir()
	seedConfig(t, home, `{"defaultAgent":"qoder","agents":{"claude":{"command":"claude-acp"},"qoder":{"command":"qoder-acp"}}}`)
	if err := ApplyAgentOverride(home, AgentClaude, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	assertRawJSONEqual(t, doc["defaultAgent"], `"qoder"`)
	var agents map[string]json.RawMessage
	if err := json.Unmarshal(doc["agents"], &agents); err != nil {
		t.Fatal(err)
	}
	if _, ok := agents[AgentClaude]; ok {
		t.Fatalf("claude entry survived delete: %s", data)
	}
	assertRawJSONEqual(t, agents[AgentQoder], `{"command":"qoder-acp"}`)
}

// Delete of an agent that is not present is a no-op: the file is not
// rewritten at all.
func TestApplyAgentOverrideDeleteAbsentEntryIsNoOp(t *testing.T) {
	home := t.TempDir()
	seed := `{"agents":{"qoder":{"command":"qoder-acp"}}}`
	seedConfig(t, home, seed)
	if err := ApplyAgentOverride(home, AgentCodex, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != seed {
		t.Fatalf("no-op delete rewrote the config: %q -> %q", seed, data)
	}
}

func TestApplyAgentOverrideMissingFile(t *testing.T) {
	home := t.TempDir()
	if err := ApplyAgentOverride(home, AgentCodex, nil); err != nil {
		t.Fatalf("delete on missing file = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".acpx")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op delete should not create .acpx: %v", err)
	}
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp"}
	if err := ApplyAgentOverride(home, AgentQoder, &qoder); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(filepath.Join(home, ".acpx"))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf(".acpx dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(configPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", fileInfo.Mode().Perm())
	}
	if _, ok, err := LoadAgentOverride(home, AgentQoder); err != nil || !ok {
		t.Fatalf("upserted override not loadable: ok=%v err=%v", ok, err)
	}
}

// Fail closed: a malformed, oversized, symlinked, or directory config must
// produce an error and be left byte-for-byte untouched.
func TestApplyAgentOverrideMalformedConfigFailsClosed(t *testing.T) {
	home := t.TempDir()
	body := `{"agents": broken`
	seedConfig(t, home, body)
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp"}
	if err := ApplyAgentOverride(home, AgentQoder, &qoder); err == nil {
		t.Fatal("malformed config accepted")
	}
	assertConfigBytes(t, home, body)
	assertNoStaleTempFiles(t, home)
}

func TestApplyAgentOverrideOversizedConfigFailsClosed(t *testing.T) {
	home := t.TempDir()
	body := `{"agents":{}}` + strings.Repeat(" ", maxAgentConfigBytes)
	seedConfig(t, home, body)
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp"}
	if err := ApplyAgentOverride(home, AgentQoder, &qoder); err == nil {
		t.Fatal("oversized config accepted")
	}
	assertConfigBytes(t, home, body)
}

func TestApplyAgentOverrideSymlinkConfigFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on windows")
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".acpx"), 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(home, "elsewhere.json")
	if err := os.WriteFile(real, []byte(`{"agents":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, configPath(home)); err != nil {
		t.Fatal(err)
	}
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp"}
	if err := ApplyAgentOverride(home, AgentQoder, &qoder); err == nil {
		t.Fatal("symlinked config accepted")
	}
	info, err := os.Lstat(configPath(home))
	if err != nil {
		t.Fatalf("symlink was replaced: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced: mode=%v", info.Mode())
	}
	if data, err := os.ReadFile(real); err != nil || string(data) != `{"agents":{}}` {
		t.Fatalf("symlink target modified: %q err=%v", data, err)
	}
}

func TestApplyAgentOverrideDirectoryConfigFailsClosed(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(configPath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp"}
	if err := ApplyAgentOverride(home, AgentQoder, &qoder); err == nil {
		t.Fatal("directory config accepted")
	}
	if info, err := os.Lstat(configPath(home)); err != nil || !info.IsDir() {
		t.Fatalf("config directory disturbed: err=%v", err)
	}
}

// After ApplyAgentOverride returns, the lock file must be unlocked and no
// temp files may be stranded in .acpx.
func TestApplyAgentOverrideReleasesLockAndLeavesNoTempFiles(t *testing.T) {
	home := t.TempDir()
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp"}
	if err := ApplyAgentOverride(home, AgentQoder, &qoder); err != nil {
		t.Fatal(err)
	}
	if err := ApplyAgentOverride(home, AgentQoder, nil); err != nil {
		t.Fatal(err)
	}
	lockPath := configPath(home) + ".lock"
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode = %v, want 0600", info.Mode().Perm())
	}
	file, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := tryLockFile(file); err != nil {
		t.Fatalf("config lock still held after ApplyAgentOverride returned: %v", err)
	}
	if err := unlockOverrideFile(file); err != nil {
		t.Fatal(err)
	}
	assertNoStaleTempFiles(t, home)
}

// A lock held by another handle must produce a bounded wait and a clear
// error, never an indefinite block.
func TestApplyAgentOverrideContendedLockTimesOut(t *testing.T) {
	home := t.TempDir()
	qoder := AgentOverride{Agent: AgentQoder, Command: "qoder-acp"}
	if err := ApplyAgentOverride(home, AgentQoder, &qoder); err != nil {
		t.Fatal(err)
	}
	lockPath := configPath(home) + ".lock"
	holder, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := tryLockFile(holder); err != nil {
		t.Fatalf("acquire lock for contention setup: %v", err)
	}
	defer unlockOverrideFile(holder)
	start := time.Now()
	err = ApplyAgentOverride(home, AgentQoder, &qoder)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("contended apply error = %v, want a lock timeout", err)
	}
	if elapsed > overrideLockTimeout+5*time.Second {
		t.Fatalf("contended apply blocked for %s, want a bounded wait", elapsed)
	}
}

func configPath(home string) string {
	return filepath.Join(home, ".acpx", "config.json")
}

func seedConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".acpx"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(home), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertConfigBytes(t *testing.T, home, want string) {
	t.Helper()
	data, err := os.ReadFile(configPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("config modified on failure path:\n got: %q\nwant: %q", data, want)
	}
}

func assertNoStaleTempFiles(t *testing.T, home string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, ".acpx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config-") {
			t.Fatalf("stale temp file left behind: %s", entry.Name())
		}
	}
}

func assertRawJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	if !bytes.Equal(compactJSON(t, got), compactJSON(t, json.RawMessage(want))) {
		t.Fatalf("raw JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	return out.Bytes()
}

func agentNames(agents map[string]json.RawMessage) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	return names
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
