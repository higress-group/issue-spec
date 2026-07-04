package commands

import "testing"

func TestResolveWriterSessionPrefersCodexThreadID(t *testing.T) {
	t.Setenv(codexThreadIDEnv, "codex-session-123")
	session := resolveWriterSession("supplied-session-456")
	if session.ID != "codex-session-123" || session.Source != codexThreadIDEnv {
		t.Fatalf("session = %+v", session)
	}
}

func TestResolveWriterSessionFallsBackToExplicitAgentSession(t *testing.T) {
	t.Setenv(codexThreadIDEnv, "")
	session := resolveWriterSession("supplied-session-456")
	if session.ID != "supplied-session-456" || session.Source != agentSessionParamSource {
		t.Fatalf("session = %+v", session)
	}
}
