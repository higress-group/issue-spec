package model

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
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
	if strings.Contains(body, "Agent Session") {
		t.Fatalf("new rationale contains deprecated session metadata: %s", body)
	}
}

func TestCodeChangeRationaleReadsLegacySessionMetadata(t *testing.T) {
	tests := map[string]CodeChangeRationaleMarker{
		"complete":       {AgentSessionID: "worker-session", AgentSessionSource: "CODEX_THREAD_ID"},
		"id only":        {AgentSessionID: "worker-session"},
		"source only":    {AgentSessionSource: "unknown-runtime"},
		"unknown source": {AgentSessionID: "worker-session", AgentSessionSource: "unknown-runtime"},
	}
	for name, session := range tests {
		t.Run(name, func(t *testing.T) {
			marker := testCodeChangeRationaleMarker()
			marker.AgentSessionID, marker.AgentSessionSource = session.AgentSessionID, session.AgentSessionSource
			body, err := RenderCodeChangeRationaleBody(marker, "legacy rationale")
			if err != nil {
				t.Fatal(err)
			}
			if name == "complete" {
				body = strings.Replace(body, "Agent Session ID: worker-session", "Agent Session ID: mismatched-visible-session", 1)
			}
			parsed, found, err := FindCodeChangeRationaleMarker(body)
			if err != nil || !found || parsed != marker {
				t.Fatalf("parsed=%+v found=%v err=%v body=%s", parsed, found, err, body)
			}
		})
	}
}

func TestCodeChangeRationaleLegacyParserIgnoresNonAuthoritativeVisibleMetadata(t *testing.T) {
	marker := testCodeChangeRationaleMarker()
	body, err := RenderCodeChangeRationaleBody(marker, "legacy rationale")
	if err != nil {
		t.Fatal(err)
	}
	body = strings.NewReplacer(
		"Process: PROCESS-001", "Process: PROCESS-999",
		"Spec: SPEC-001", "Spec: SPEC-999",
		"Spec Comment: https://issues.example.test/acme/widgets/issues/1#issuecomment-2", "Spec Comment: altered-visible-value",
		"Provider: code.example", "Provider: altered.example",
		"External Repository: acme/widgets-code", "External Repository: altered/repository",
		"Change: change opaque/1", "Change: altered-change",
		"Reference Version: 7", "Reference Version: 99",
	).Replace(body)
	parsed, found, err := FindCodeChangeRationaleMarker(body)
	if err != nil || !found || parsed != marker {
		t.Fatalf("parsed=%+v found=%v err=%v body=%s", parsed, found, err, body)
	}
}

func TestCodeChangeRationaleStrictParserRejectsMutation(t *testing.T) {
	body, err := RenderCodeChangeRationaleBody(testCodeChangeRationaleMarker(), "why")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"duplicate":            body + "\n" + strings.Split(body, "\n")[0],
		"unknown attribute":    strings.Replace(body, " version=1", " extra=x version=1", 1),
		"bad base64":           strings.Replace(body, "payload=", "payload=%", 1),
		"visible agent":        strings.Replace(body, "Agent: Worker", "Agent: Coordinator", 1),
		"visible revision":     strings.Replace(body, "Subject Revision: head-abc", "Subject Revision: head-other", 1),
		"trailing payload":     strings.Replace(body, "payload=", "payload=eyJ4IjoxfQ", 1),
		"noncanonical version": strings.Replace(body, "version=1", "version=01", 1),
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
		"process":  func(marker *CodeChangeRationaleMarker) { marker.Process = "PROCESS-x" },
		"spec":     func(marker *CodeChangeRationaleMarker) { marker.Spec = "SPEC-x" },
		"spec url": func(marker *CodeChangeRationaleMarker) { marker.SpecURL = "relative" },
		"provider": func(marker *CodeChangeRationaleMarker) { marker.ProviderKey = "" },
		"version":  func(marker *CodeChangeRationaleMarker) { marker.ReferenceVersion = 0 },
		"revision": func(marker *CodeChangeRationaleMarker) { marker.SubjectRevision = "" },
		"agent":    func(marker *CodeChangeRationaleMarker) { marker.Agent = "" },
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

func TestCodeChangeRationaleVersion2StateMatrixAndProjection(t *testing.T) {
	base := testCodeChangeRationaleMarker()
	const rationale = "The carrier remains authoritative while provider publication is recoverable."
	pending, err := PrepareCodeChangeRationaleMarker(base, rationale, CodeChangeRationalePendingExternal, "", "")
	if err != nil {
		t.Fatal(err)
	}
	published, err := PrepareCodeChangeRationaleMarker(base, rationale, CodeChangeRationalePublishedExternal,
		"comment-17", "https://code.example/acme/widgets/comments/17")
	if err != nil {
		t.Fatal(err)
	}
	unavailable, err := PrepareCodeChangeRationaleMarker(base, rationale, CodeChangeRationaleExternalUnavailable, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if pending.RationaleID == "" || pending.RationaleID != published.RationaleID ||
		pending.RationaleID != unavailable.RationaleID {
		t.Fatalf("state transitions changed rationale identity: pending=%q published=%q unavailable=%q",
			pending.RationaleID, published.RationaleID, unavailable.RationaleID)
	}
	for name, marker := range map[string]CodeChangeRationaleMarker{
		"pending": pending, "published": published, "unavailable": unavailable,
	} {
		t.Run(name, func(t *testing.T) {
			body, err := RenderCodeChangeRationaleBody(marker, rationale)
			if err != nil {
				t.Fatal(err)
			}
			parsed, found, err := FindCodeChangeRationaleMarker(body)
			if err != nil || !found || !reflect.DeepEqual(parsed, marker) {
				t.Fatalf("parsed=%+v found=%v err=%v", parsed, found, err)
			}
			if CodeChangeRationaleVersion(parsed) != 2 {
				t.Fatalf("version=%d", CodeChangeRationaleVersion(parsed))
			}
		})
	}
	if CodeChangeRationaleGateEligible(pending) || !CodeChangeRationaleGateEligible(published) ||
		!CodeChangeRationaleGateEligible(unavailable) || !CodeChangeRationaleGateEligible(base) {
		t.Fatal("pending/published/unavailable/legacy gate eligibility is incorrect")
	}
	pendingProjection, err := RenderCodeChangeRationaleExternalProjection(pending, rationale)
	if err != nil {
		t.Fatal(err)
	}
	publishedProjection, err := RenderCodeChangeRationaleExternalProjection(published, rationale)
	if err != nil || pendingProjection != publishedProjection {
		t.Fatalf("projection changed across state transition: err=%v\npending=%s\npublished=%s", err, pendingProjection, publishedProjection)
	}
	for _, forbidden := range []string{"pending_external", "published_external", "comment-17", "Agent Session"} {
		if strings.Contains(pendingProjection, forbidden) {
			t.Fatalf("projection contains mutable or runtime field %q:\n%s", forbidden, pendingProjection)
		}
	}
}

func TestCodeChangeRationaleIdentityIsStableAndSensitive(t *testing.T) {
	base := testCodeChangeRationaleMarker()
	first, err := ComputeCodeChangeRationaleID(base, " why ")
	if err != nil {
		t.Fatal(err)
	}
	retry := base
	retry.AgentSessionID, retry.AgentSessionSource = "runtime-1", "runtime"
	second, err := ComputeCodeChangeRationaleID(retry, "why")
	if err != nil || first != second {
		t.Fatalf("runtime identity affected logical identity: first=%q second=%q err=%v", first, second, err)
	}
	mutations := map[string]func(*CodeChangeRationaleMarker){
		"provider":   func(marker *CodeChangeRationaleMarker) { marker.ProviderKey = "other.example" },
		"repository": func(marker *CodeChangeRationaleMarker) { marker.ExternalRepository += "-other" },
		"change":     func(marker *CodeChangeRationaleMarker) { marker.ChangeID += "-other" },
		"version":    func(marker *CodeChangeRationaleMarker) { marker.ReferenceVersion++ },
		"revision":   func(marker *CodeChangeRationaleMarker) { marker.SubjectRevision += "-other" },
		"process":    func(marker *CodeChangeRationaleMarker) { marker.Process = "PROCESS-002" },
		"spec":       func(marker *CodeChangeRationaleMarker) { marker.Spec = "SPEC-002" },
		"spec url":   func(marker *CodeChangeRationaleMarker) { marker.SpecURL += "-other" },
		"agent":      func(marker *CodeChangeRationaleMarker) { marker.Agent += " Other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			got, err := ComputeCodeChangeRationaleID(changed, "why")
			if err != nil {
				t.Fatal(err)
			}
			if got == first {
				t.Fatalf("%s did not affect rationale identity", name)
			}
		})
	}
	if changed, err := ComputeCodeChangeRationaleID(base, "why changed"); err != nil || changed == first {
		t.Fatalf("body sensitivity: id=%q err=%v", changed, err)
	}
}

func TestCodeChangeRationaleVersion2RejectsMalformedStateAndBody(t *testing.T) {
	base := testCodeChangeRationaleMarker()
	const rationale = "why"
	tests := []struct {
		name        string
		state       string
		externalID  string
		externalURL string
	}{
		{name: "unknown state", state: "complete"},
		{name: "pending receipt", state: CodeChangeRationalePendingExternal, externalID: "comment-1", externalURL: "https://code.example/comment/1"},
		{name: "published missing id", state: CodeChangeRationalePublishedExternal, externalURL: "https://code.example/comment/1"},
		{name: "published unsafe url", state: CodeChangeRationalePublishedExternal, externalID: "comment-1", externalURL: "https://code.example/comment/1?token=secret"},
		{name: "unavailable receipt", state: CodeChangeRationaleExternalUnavailable, externalID: "comment-1", externalURL: "https://code.example/comment/1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PrepareCodeChangeRationaleMarker(base, rationale, test.state, test.externalID, test.externalURL); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	pending, err := PrepareCodeChangeRationaleMarker(base, rationale, CodeChangeRationalePendingExternal, "", "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderCodeChangeRationaleBody(pending, rationale)
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]string{
		"changed body":         strings.Replace(body, "\nwhy\n", "\nother\n", 1),
		"changed rationale id": strings.Replace(body, "Rationale ID: "+pending.RationaleID, "Rationale ID: issue-spec-rationale-sha256:"+strings.Repeat("0", 64), 1),
		"changed process":      strings.Replace(body, "Process: PROCESS-001", "Process: PROCESS-999", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, found, err := FindCodeChangeRationaleMarker(candidate); !found || err == nil {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
	pending.AgentSessionID = "runtime"
	if _, err := RenderCodeChangeRationaleBody(pending, rationale); err == nil {
		t.Fatal("version-2 carrier accepted runtime session identity")
	}
}

func testCodeChangeRationaleMarker() CodeChangeRationaleMarker {
	return CodeChangeRationaleMarker{Process: "PROCESS-001", Spec: "SPEC-001",
		SpecURL:     "https://issues.example.test/acme/widgets/issues/1#issuecomment-2",
		ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change opaque/1",
		ReferenceVersion: 7, SubjectRevision: "head-abc", Agent: "Worker"}
}
