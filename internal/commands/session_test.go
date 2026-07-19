package commands

import "testing"

func TestResolveWriterSessionIgnoresRuntimeAndDeprecatedFlag(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "codex-session-123")
	session := resolveWriterSession("supplied-session-456")
	if session.ID != "" || session.Source != "" {
		t.Fatalf("session = %+v", session)
	}
}
