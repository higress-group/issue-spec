package state

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const processWorkspaceTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestProcessWorkspaceAssociationRoundTripIsPortable(t *testing.T) {
	association := testProcessWorkspaceAssociation("ws-1", "PROCESS-001")
	encoded, err := json.Marshal(association)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"worktree_path", "integration_root", `"owner":`, `"token":`, "credential", "/private/tmp"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("portable association leaked %q: %s", forbidden, encoded)
		}
	}
	var decoded ProcessWorkspaceAssociation
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Lifecycle != ProcessWorkspaceAllocating || decoded.Branch != "process/one" || decoded.Provider.ProviderKey != "github" {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func TestProcessWorkspaceAssociationLifecycleCASAndResourceRelease(t *testing.T) {
	associations := NewProcessWorkspaceAssociations()
	first := testProcessWorkspaceAssociation("ws-1", "PROCESS-001")
	first.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "database", Name: "integration", Exclusive: true}}
	finalizeAssociation(&first)
	reserved, err := associations.Reserve(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := associations.Reserve(first); err != nil {
		t.Fatalf("idempotent reserve: %v", err)
	}
	if _, err := associations.Transition(first.WorkspaceID, "wrong", ProcessWorkspaceAllocating, ProcessWorkspacePrepared); err == nil {
		t.Fatal("wrong reservation CAS accepted")
	}
	prepared, err := associations.Transition(first.WorkspaceID, reserved.ReservationID, ProcessWorkspaceAllocating, ProcessWorkspacePrepared)
	if err != nil || prepared.Lifecycle != ProcessWorkspacePrepared {
		t.Fatalf("prepared=%+v err=%v", prepared, err)
	}
	if _, err := associations.ConfirmReleased(first.WorkspaceID, reserved.ReservationID); err == nil {
		t.Fatal("release skipped cleanup-pending")
	}
	if _, err := associations.Transition(first.WorkspaceID, reserved.ReservationID, ProcessWorkspacePrepared, ProcessWorkspaceCleanupPending); err != nil {
		t.Fatal(err)
	}
	released, err := associations.ConfirmReleased(first.WorkspaceID, reserved.ReservationID)
	if err != nil || released.Lifecycle != ProcessWorkspaceReleased {
		t.Fatalf("released=%+v err=%v", released, err)
	}
	second := testProcessWorkspaceAssociation("ws-2", "PROCESS-002")
	second.RuntimeResources = append([]processworkspace.RuntimeResource(nil), first.RuntimeResources...)
	finalizeAssociation(&second)
	if _, err := associations.Reserve(second); err != nil {
		t.Fatalf("released resource remained reserved: %v", err)
	}
	if err := associations.Delete(first.WorkspaceID, "wrong"); err == nil {
		t.Fatal("wrong delete CAS accepted")
	}
	if err := associations.Delete(first.WorkspaceID, first.ReservationID); err != nil {
		t.Fatal(err)
	}
}

func TestProcessWorkspaceAssociationCanonicalResourcesFailClosed(t *testing.T) {
	for _, resources := range [][]processworkspace.RuntimeResource{
		{{Kind: "Port", Name: "http"}}, {{Kind: "port", Name: "HTTP"}},
		{{Kind: "port", Name: "z"}, {Kind: "port", Name: "a"}},
		{{Kind: "port", Name: "http"}, {Kind: "port", Name: "http"}},
	} {
		association := testProcessWorkspaceAssociation("ws-case", "PROCESS-001")
		association.RuntimeResources = resources
		finalizeAssociation(&association)
		if err := association.Validate(); err == nil {
			t.Fatalf("non-canonical resources accepted: %+v", resources)
		}
	}
	associations := NewProcessWorkspaceAssociations()
	first := testProcessWorkspaceAssociation("ws-a", "PROCESS-001")
	first.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "port", Name: "http", Exclusive: true}}
	finalizeAssociation(&first)
	if _, err := associations.Reserve(first); err != nil {
		t.Fatal(err)
	}
	caseVariant := testProcessWorkspaceAssociation("ws-b", "PROCESS-002")
	caseVariant.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "port", Name: "HTTP"}}
	finalizeAssociation(&caseVariant)
	if _, err := associations.Reserve(caseVariant); err == nil {
		t.Fatal("case variant bypassed canonical collision domain")
	}
}

func TestProcessWorkspaceAssociationRepositoryAndProviderIdentity(t *testing.T) {
	for _, repository := range []string{"/tmp/repo", "C:/tmp/repo", "https://user:token@example/repo", "file:///tmp/repo", "o/../r", `o\\r`, "./repo"} {
		association := testProcessWorkspaceAssociation("ws-repo", "PROCESS-001")
		association.Repository = repository
		finalizeAssociation(&association)
		if err := association.Validate(); err == nil {
			t.Fatalf("repository %q accepted", repository)
		}
	}
	for _, provider := range []ProcessWorkspaceProviderIdentity{
		{ProviderKey: "GitHub", ServerInstance: "public", Host: "github.com"},
		{ProviderKey: "github", ServerInstance: "https://github.com", Host: "github.com"},
		{ProviderKey: "github", ServerInstance: "public", Host: "user@github.com"},
	} {
		association := testProcessWorkspaceAssociation("ws-provider", "PROCESS-001")
		association.Provider = provider
		finalizeAssociation(&association)
		if err := association.Validate(); err == nil {
			t.Fatalf("provider %+v accepted", provider)
		}
	}
}

func TestProcessWorkspaceAssociationModeNoneCanReserveRuntimeResource(t *testing.T) {
	association := testProcessWorkspaceAssociation("ws-none", "PROCESS-009")
	association.ExecutionClass, association.Mode = processworkspace.ExecutionOrchestration, processworkspace.ModeNone
	association.BaseSHA, association.Branch, association.LocalAssociationRef = "", "", ""
	association.WriteOwnership, association.SharedTouchpoints = nil, nil
	association.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "database", Name: "external", Exclusive: true}}
	finalizeAssociation(&association)
	if err := association.Validate(); err != nil {
		t.Fatal(err)
	}
	associations := NewProcessWorkspaceAssociations()
	if _, err := associations.Reserve(association); err != nil {
		t.Fatal(err)
	}
}

func TestProcessWorkspaceAssociationLegacyAndFutureSchemas(t *testing.T) {
	legacy := `{"workspace_id":"ws-1","repository":"o/r","process_id":"PROCESS-001","base_sha":"` + processWorkspaceTestSHA + `","execution_class":"review","mode":"snapshot","local_association_ref":"lease:ws-1","runtime_namespace":"process-legacy"}`
	var association ProcessWorkspaceAssociation
	if err := json.Unmarshal([]byte(legacy), &association); err != nil {
		t.Fatalf("legacy decode failed: %v", err)
	}
	if association.SchemaVersion != ProcessWorkspaceAssociationSchemaVersion || association.Lifecycle != ProcessWorkspaceFailed {
		t.Fatalf("legacy=%+v", association)
	}
	future := strings.Replace(legacy, `{"workspace_id"`, `{"schema_version":3,"workspace_id"`, 1)
	if err := json.Unmarshal([]byte(future), &association); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("future association err=%v", err)
	}
	var collection ProcessWorkspaceAssociations
	if err := json.Unmarshal([]byte(`{"schema_version":3,"by_workspace":{}}`), &collection); err == nil {
		t.Fatal("future collection accepted")
	}
}

func testProcessWorkspaceAssociation(workspaceID, processID string) ProcessWorkspaceAssociation {
	association := ProcessWorkspaceAssociation{
		SchemaVersion: ProcessWorkspaceAssociationSchemaVersion, Lifecycle: ProcessWorkspaceAllocating,
		WorkspaceID: workspaceID, Repository: "o/r", Provider: ProcessWorkspaceProviderIdentity{ProviderKey: "github", ServerInstance: "public", Host: "github.com"},
		ProcessID: processID, BaseSHA: processWorkspaceTestSHA, Branch: "process/one", ExecutionClass: processworkspace.ExecutionChangeBearing,
		Mode: processworkspace.ModeWritable, WriteOwnership: []string{"internal/**"}, LocalAssociationRef: "lease:" + workspaceID, RuntimeNamespace: "process-" + strings.ToLower(workspaceID),
	}
	finalizeAssociation(&association)
	return association
}

func finalizeAssociation(association *ProcessWorkspaceAssociation) {
	association.ReservationIdentity = association.ExpectedReservationIdentity()
	association.ReservationID = "reservation:" + strings.ToLower(association.WorkspaceID)
}
