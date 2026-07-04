package model

import "testing"

func TestRenderAndFindRationaleSessionMetadata(t *testing.T) {
	body, err := RenderRationaleBodyWithSession("Worker", "codex-session-123", "CODEX_THREAD_ID", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "why", "a.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	marker, ok, err := FindRationaleMarker(body)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("missing rationale marker")
	}
	if marker.Agent != "Worker" || marker.AgentSessionID != "codex-session-123" || marker.AgentSessionSource != "CODEX_THREAD_ID" {
		t.Fatalf("unexpected metadata: %+v", marker)
	}
}
