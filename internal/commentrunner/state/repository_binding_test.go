package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	resolver "github.com/higress-group/issue-spec/internal/commentrunner/repository"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestRepositoryBindingSnapshotStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	binding := state.RepositoryBindingSnapshot{Source: "operator", IssueRepositoryKey: "o/r", BindingID: "mapping-1",
		Version: 2, ProviderKey: "github", ExternalRepositoryID: "o/r", CloneURL: "https://code.example/o/r.git",
		WebURL: "https://code.example/o/r", DefaultBranch: "main"}
	workspace := state.WorkspaceMetadata{ID: "ws-1", Path: "/tmp/ws-1", Repo: "o/r", RepositoryBinding: binding}
	st := state.NewState()
	if err := st.UpsertWorkspace(workspace); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertJob(state.Job{ID: "job-1", Repo: "o/r", Status: state.StatusRunning,
		RepositoryBinding: binding, Workspace: workspace, DispatchIntent: state.DispatchIntent{RepositoryBinding: binding}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPublicSession(state.PublicSession{Repo: "o/r", PublicSessionID: "ps-1", AcpxRecordID: "rec-1",
		Status: state.StatusRunning, RepositoryBinding: binding, Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveFile(path, st); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	job := loaded.Jobs["job-1"]
	session, _ := loaded.GetPublicSession("o/r", "ps-1")
	storedWorkspace, _ := loaded.GetWorkspace("ws-1")
	for label, got := range map[string]state.RepositoryBindingSnapshot{
		"job": job.RepositoryBinding, "dispatch": job.DispatchIntent.RepositoryBinding,
		"session": session.RepositoryBinding, "workspace": storedWorkspace.RepositoryBinding,
	} {
		if !got.Equal(binding) {
			t.Errorf("%s binding=%+v, want %+v", label, got, binding)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "secret", "credential"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("state snapshot contains forbidden credential field %q: %s", forbidden, raw)
		}
	}
}

func TestLegacyStateLoadsButResumeBindingFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"schema_version":1,"public_sessions":{"o/r#ps-1":{"repo":"o/r","public_session_id":"ps-1","acpx_record_id":"rec-1","status":"completed","workspace":{"id":"ws-1","path":"/tmp/ws-1","repo":"o/r","clone_url":"https://github.com/o/r.git"}}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != state.SchemaVersion {
		t.Fatalf("schema version=%d, want %d", loaded.SchemaVersion, state.SchemaVersion)
	}
	session, ok := loaded.GetPublicSession("o/r", "ps-1")
	if !ok {
		t.Fatal("legacy session was not loaded")
	}
	if err := resolver.ValidatePinned(session.RepositoryBinding, state.RepositoryBindingSnapshot{}); resolver.DiagnosticCode(err) != resolver.DiagnosticLegacyState {
		t.Fatalf("legacy binding validation error=%v", err)
	}
}
