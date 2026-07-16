package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const workspaceBaseSHA = "1111111111111111111111111111111111111111"

func testProcessWorkspace(id string, class ProcessExecutionClass) ProcessWorkspace {
	now := time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)
	workspace := ProcessWorkspace{
		SchemaVersion:  processworkspace.LeaseSchemaVersion,
		WorkspaceID:    "ws-" + strings.ToLower(id),
		Repository:     "higress-group/issue-spec",
		ProcessID:      id,
		ExecutionClass: processworkspace.ExecutionClass(class),
		State:          processworkspace.StatePrepared,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	switch class {
	case ProcessExecutionChangeBearing:
		workspace.Mode = processworkspace.ModeWritable
		workspace.BaseSHA = workspaceBaseSHA
		workspace.Branch = "codex/" + strings.ToLower(id)
		workspace.WriteOwnership = []string{"internal/model/**"}
		workspace.RuntimeNamespace = "runtime-" + strings.ToLower(id)
	case ProcessExecutionReview, ProcessExecutionVerification:
		workspace.Mode = processworkspace.ModeSnapshot
		workspace.BaseSHA = workspaceBaseSHA
		workspace.DetachedRevision = workspaceBaseSHA
		workspace.RuntimeNamespace = "runtime-" + strings.ToLower(id)
	case ProcessExecutionOrchestration, ProcessExecutionExternal:
		workspace.Mode = processworkspace.ModeNone
	}
	return workspace
}

func processBodyWithWorkspace(id string, class ProcessExecutionClass, workspace ProcessWorkspace) string {
	section, err := RenderProcessWorkspaceSection(workspace)
	if err != nil {
		panic(err)
	}
	return "## Process: test\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- " + string(class) + "\n\n" + section + "\n\n### Handoff\n\nN/A"
}

func TestProcessWorkspaceCanonicalRoundTripByExecutionClass(t *testing.T) {
	for _, class := range []ProcessExecutionClass{
		ProcessExecutionChangeBearing,
		ProcessExecutionReview,
		ProcessExecutionVerification,
		ProcessExecutionOrchestration,
		ProcessExecutionExternal,
	} {
		t.Run(string(class), func(t *testing.T) {
			workspace := testProcessWorkspace("PROCESS-003", class)
			body := processBodyWithWorkspace("PROCESS-003", class, workspace)
			result := ParseProcessWorkspace("PROCESS-003", "https://example.test/process", body)
			if result.Blocking() || !result.Explicit || result.Workspace == nil {
				t.Fatalf("parse result = %+v", result)
			}
			got, _ := json.Marshal(result.Workspace)
			want, _ := json.Marshal(workspace)
			if string(got) != string(want) {
				t.Fatalf("round trip mismatch\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestExternalProcessWorkspaceRejectsCheckoutModesInRenderingAndParsing(t *testing.T) {
	external := testProcessWorkspace("PROCESS-EXT", ProcessExecutionExternal)
	valid, err := RenderProcessWorkspaceSection(external)
	if err != nil {
		t.Fatalf("render external no-checkout workspace: %v", err)
	}
	for _, mode := range []processworkspace.WorkspaceMode{processworkspace.ModeWritable, processworkspace.ModeSnapshot} {
		t.Run(string(mode), func(t *testing.T) {
			invalid := external
			invalid.Mode = mode
			if _, err := RenderProcessWorkspaceSection(invalid); err == nil || !strings.Contains(err.Error(), "external execution requires no-checkout workspace mode") {
				t.Fatalf("render external %s mode error=%v", mode, err)
			}
			section := strings.Replace(valid, `"mode": "none"`, `"mode": "`+string(mode)+`"`, 1)
			body := "## Process: external\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- external\n\n" + section + "\n\n### Handoff\n\nN/A"
			parsed := ParseProcessWorkspace("PROCESS-EXT", "", body)
			if !parsed.Blocking() || parsed.Workspace != nil || len(parsed.Diagnostics) != 1 ||
				parsed.Diagnostics[0].Element != "workspace-invalid" || !strings.Contains(parsed.Diagnostics[0].Message, "external execution requires no-checkout workspace mode") {
				t.Fatalf("parse accepted external %s mode: %+v", mode, parsed)
			}
		})
	}
}

func TestProcessWorkspaceLegacyMissingSectionRemainsCompatible(t *testing.T) {
	body := "## Process: old\n\n### Parent TASK\n\n- TASK-001\n\n### Handoff\n\nN/A"
	result := ParseProcessWorkspace("PROCESS-OLD", "", body)
	if result.Explicit || result.Blocking() || result.Workspace != nil || len(result.Diagnostics) != 1 {
		t.Fatalf("legacy result = %+v", result)
	}
	if result.Diagnostics[0].Severity != "warning" || result.Diagnostics[0].Element != "workspace-missing" {
		t.Fatalf("legacy diagnostic = %+v", result.Diagnostics[0])
	}
}

func TestProcessWorkspaceRejectsFutureCorruptDuplicateAndLocalFields(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-003", ProcessExecutionChangeBearing)
	valid, err := RenderProcessWorkspaceSection(workspace)
	if err != nil {
		t.Fatal(err)
	}
	future := strings.Replace(valid, `"schema_version": 1`, `"schema_version": 2`, 1)
	local := strings.Replace(valid, `"workspace_id":`, "\"worktree_path\": \"/private/tmp/leak\",\n  \"workspace_id\":", 1)
	tests := map[string]struct {
		section string
		element string
	}{
		"future schema": {future, "workspace-schema-unsupported"},
		"corrupt json":  {"### Workspace\n\n```json\n{nope}\n```", "workspace-invalid"},
		"duplicate":     {valid + "\n\n" + valid, "workspace-duplicate"},
		"local field":   {local, "workspace-local-field"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			body := "## Process: test\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- change-bearing\n\n" + test.section + "\n\n### Handoff\n\nN/A"
			result := ParseProcessWorkspace("PROCESS-003", "", body)
			if !result.Blocking() || len(result.Diagnostics) != 1 || result.Diagnostics[0].Element != test.element {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestProcessWorkspaceRejectsClassAndIdentityMismatch(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-OTHER", ProcessExecutionReview)
	body := processBodyWithWorkspace("PROCESS-OTHER", ProcessExecutionChangeBearing, workspace)
	result := ParseProcessWorkspace("PROCESS-003", "", body)
	if !result.Blocking() || len(result.Diagnostics) != 1 || result.Diagnostics[0].Element != "workspace-process-mismatch" {
		t.Fatalf("identity result = %+v", result)
	}

	workspace.ProcessID = "PROCESS-003"
	body = processBodyWithWorkspace("PROCESS-003", ProcessExecutionChangeBearing, workspace)
	result = ParseProcessWorkspace("PROCESS-003", "", body)
	if !result.Blocking() || len(result.Diagnostics) != 1 || result.Diagnostics[0].Element != "workspace-class-mismatch" {
		t.Fatalf("class result = %+v", result)
	}
}

func TestRenderedProcessWorkspaceJSONIsPortable(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-003", ProcessExecutionChangeBearing)
	section, err := RenderProcessWorkspaceSection(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/private/", "/Users/", "worktree_path", "integration_root", "git_common_dir", "lock_token", "credential", "hostname", `"pid"`} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("portable section contains %q:\n%s", forbidden, section)
		}
	}
	workspace.Repository = "/Users/example/issue-spec"
	if _, err := RenderProcessWorkspaceSection(workspace); err == nil || !strings.Contains(err.Error(), "portable") {
		t.Fatalf("expected absolute repository path rejection, got %v", err)
	}
}

func TestProcessWorkspaceRepositoryIdentityMatrix(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-003", ProcessExecutionChangeBearing)
	for _, repository := range []string{"owner/repo", "owner/.repo", "group/subgroup/repo", "github.com:owner/repo", "aone-bridge:project/repo"} {
		workspace.Repository = repository
		if _, err := RenderProcessWorkspaceSection(workspace); err != nil {
			t.Errorf("portable repository %q rejected: %v", repository, err)
		}
	}
	for _, repository := range []string{"./repo", "../private/repo", "owner/../repo", `owner\repo`, `C:relative`, `C:/absolute`, "file:relative", "FILE:///tmp/repo", "/tmp/repo", "repo"} {
		workspace.Repository = repository
		if _, err := RenderProcessWorkspaceSection(workspace); err == nil {
			t.Errorf("local or ambiguous repository %q accepted", repository)
		}
	}
}

func TestProcessWorkspaceLocalFieldDetectionDoesNotInspectValues(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-003", ProcessExecutionChangeBearing)
	workspace.Branch = "credential"
	workspace.RuntimeNamespace = "hostname"
	workspace.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "credential", Name: "pid"}}
	body := processBodyWithWorkspace("PROCESS-003", ProcessExecutionChangeBearing, workspace)
	parsed := ParseProcessWorkspace("PROCESS-003", "", body)
	if parsed.Blocking() || parsed.Workspace == nil || parsed.Workspace.Branch != "credential" || parsed.Workspace.RuntimeNamespace != "hostname" {
		t.Fatalf("portable values failed round trip: %+v", parsed)
	}
}

func TestProcessWorkspaceIgnoresHeadingsInsideCodeExamples(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-003", ProcessExecutionChangeBearing)
	actual := mustWorkspaceSection(t, workspace)
	examples := "### Scope\n\n````markdown\n### Workspace\n\n```json\n{}\n```\n````\n\n~~~markdown\n### Workspace\n~~~\n\n    ### Workspace\n    example only\n\n"
	body := "## Process: test\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- change-bearing\n\n" + examples + actual + "\n\n### Handoff\n\nN/A"
	parsed := ParseProcessWorkspace("PROCESS-003", "", body)
	if parsed.Blocking() || parsed.Workspace == nil || parsed.Workspace.WorkspaceID != workspace.WorkspaceID {
		t.Fatalf("code example headings changed Workspace parsing: %+v", parsed)
	}
}

func TestProcessWorkspaceRejectsDuplicateJSONKeysAtEveryDepth(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-003", ProcessExecutionChangeBearing)
	workspace.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "port", Name: "http"}}
	valid := mustWorkspaceSection(t, workspace)
	tests := map[string]string{
		"schema version":          strings.Replace(valid, `"schema_version": 1`, "\"schema_version\": 2,\n  \"schema_version\": 1", 1),
		"repository local path":   strings.Replace(valid, `"repository": "higress-group/issue-spec"`, "\"repository\": \"/Users/example/issue-spec\",\n  \"repository\": \"higress-group/issue-spec\"", 1),
		"nested runtime resource": strings.Replace(valid, `"kind": "port"`, "\"kind\": \"port\",\n      \"kind\": \"socket\"", 1),
		"case-folded repository shadow": strings.Replace(valid, `"repository": "higress-group/issue-spec"`,
			"\"Repository\": \"/Users/example/private\",\n  \"repository\": \"higress-group/issue-spec\"", 1),
		"case-folded nested kind shadow": strings.Replace(valid, `"kind": "port"`,
			"\"Kind\": \"credential\",\n      \"kind\": \"port\"", 1),
		"unicode kelvin kind shadow": strings.Replace(valid, `"kind": "port"`,
			"\"Kind\": \"credential\",\n      \"kind\": \"port\"", 1),
		"unicode long-s schema shadow": strings.Replace(valid, `"schema_version": 1`,
			"\"ſchema_version\": 2,\n  \"schema_version\": 1", 1),
	}
	for name, section := range tests {
		t.Run(name, func(t *testing.T) {
			body := "## Process: test\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- change-bearing\n\n" + section + "\n\n### Handoff\n\nN/A"
			parsed := ParseProcessWorkspace("PROCESS-003", "", body)
			if !parsed.Blocking() || len(parsed.Diagnostics) != 1 || parsed.Diagnostics[0].Element != "workspace-invalid" || !strings.Contains(parsed.Diagnostics[0].Message, "duplicate object key") {
				t.Fatalf("duplicate keys accepted: %+v", parsed)
			}
		})
	}
}
