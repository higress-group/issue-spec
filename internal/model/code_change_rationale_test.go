package model

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestCodeChangeRationaleRoundTrip(t *testing.T) {
	marker := testCodeChangeRationaleMarker()
	body, err := RenderCodeChangeRationaleBody(marker, "This implementation keeps the provider boundary explicit.")
	if err != nil {
		t.Fatal(err)
	}
	parsed, found, err := FindCodeChangeRationaleMarker(body)
	if err != nil || !found || parsed != marker {
		t.Fatalf("parsed=%+v found=%v err=%v", parsed, found, err)
	}
	if !IsLikelyCodeChangeRationale(body) || !strings.Contains(body, "### Rationale") {
		t.Fatalf("rationale body is incomplete: %s", body)
	}
}

func TestCodeChangeRationaleStrictParserRejectsMutation(t *testing.T) {
	body, err := RenderCodeChangeRationaleBody(testCodeChangeRationaleMarker(), "why")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"duplicate":         body + "\n" + strings.Split(body, "\n")[0],
		"unknown attribute": strings.Replace(body, " version=1", " extra=x version=1", 1),
		"bad base64":        strings.Replace(body, "payload=", "payload=%", 1),
		"visible agent":     strings.Replace(body, "Agent: Worker", "Agent: Coordinator", 1),
		"visible session":   strings.Replace(body, "Agent Session ID: worker-session", "Agent Session ID: coordinator-session", 1),
		"trailing payload":  strings.Replace(body, "payload=", "payload=eyJ4IjoxfQ", 1),
	}
	noncanonical := testCodeChangeRationaleMarker()
	noncanonical.Agent = " Worker "
	raw, err := json.Marshal(noncanonical)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(body, "\n")
	fields[0] = "<!-- issue-spec:code-change-rationale payload=" + base64.RawURLEncoding.EncodeToString(raw) + " version=1 -->"
	tests["noncanonical identity"] = strings.Join(fields, "\n")
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, found, err := FindCodeChangeRationaleMarker(candidate); !found || err == nil {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
}

func TestCodeChangeRationaleRequiresCompleteIdentity(t *testing.T) {
	tests := map[string]func(*CodeChangeRationaleMarker){
		"process":        func(marker *CodeChangeRationaleMarker) { marker.Process = "PROCESS-x" },
		"spec":           func(marker *CodeChangeRationaleMarker) { marker.Spec = "SPEC-x" },
		"spec url":       func(marker *CodeChangeRationaleMarker) { marker.SpecURL = "relative" },
		"provider":       func(marker *CodeChangeRationaleMarker) { marker.ProviderKey = "" },
		"version":        func(marker *CodeChangeRationaleMarker) { marker.ReferenceVersion = 0 },
		"revision":       func(marker *CodeChangeRationaleMarker) { marker.SubjectRevision = "" },
		"agent":          func(marker *CodeChangeRationaleMarker) { marker.Agent = "" },
		"session":        func(marker *CodeChangeRationaleMarker) { marker.AgentSessionID = "" },
		"session source": func(marker *CodeChangeRationaleMarker) { marker.AgentSessionSource = "body" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			marker := testCodeChangeRationaleMarker()
			mutate(&marker)
			if _, err := RenderCodeChangeRationaleBody(marker, "why"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func testCodeChangeRationaleMarker() CodeChangeRationaleMarker {
	return CodeChangeRationaleMarker{Process: "PROCESS-001", Spec: "SPEC-001",
		SpecURL:     "https://issues.example.test/acme/widgets/issues/1#issuecomment-2",
		ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change opaque/1",
		ReferenceVersion: 7, SubjectRevision: "head-abc", Agent: "Worker", AgentSessionID: "worker-session",
		AgentSessionSource: "CODEX_THREAD_ID"}
}
