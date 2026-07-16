package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolvePrefersIssueSpecConfigOverLegacyOpenSpec(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeFile(t, filepath.Join(root, "openspec", "config.yaml"), "schema: legacy\n")
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: proposal.md
`)
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "proposal.md"), "# Proposal {{.Change}}\n")

	plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
	if err != nil {
		t.Fatalf("Resolve returned error: %v diagnostics=%+v", err, plan.Diagnostics)
	}
	if plan.Source.Kind != SourceIssueSpecProject {
		t.Fatalf("source kind = %q, want issue-spec project", plan.Source.Kind)
	}
	if plan.Source.SchemaName != "custom" {
		t.Fatalf("schema = %q, want custom", plan.Source.SchemaName)
	}
	if _, ok := plan.ArtifactForIssue("proposal"); !ok {
		t.Fatalf("proposal template not resolved: %+v", plan.Artifacts)
	}
	if !hasDiagnostic(plan.Diagnostics, "legacy_config_ignored") {
		t.Fatalf("expected legacy ignored diagnostic: %+v", plan.Diagnostics)
	}
}

func TestResolveUsesLegacyOpenSpecWhenNoPreferredConfigExists(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "openspec", "config.yaml"), "schema: istio-agent-workflow\n")
	writeFile(t, filepath.Join(root, "openspec", "schemas", "istio-agent-workflow", "schema.yaml"), `
artifacts:
  specs:
    type: specs
    generates: specs/**/*.md
    template: spec.md
  tasks:
    type: tasks
    generates: tasks.md
    apply:
      tracks: tasks.md
  review:
    type: review
    generates: review.md
`)
	writeFile(t, filepath.Join(root, "openspec", "schemas", "istio-agent-workflow", "templates", "spec.md"), "## Requirement: {{.Input.requirement.title}}\n")

	plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
	if err != nil {
		t.Fatalf("Resolve returned error: %v diagnostics=%+v", err, plan.Diagnostics)
	}
	if plan.Source.Kind != SourceLegacyOpenSpec {
		t.Fatalf("source kind = %q, want legacy", plan.Source.Kind)
	}
	if !hasDiagnostic(plan.Diagnostics, "legacy_openspec_mode") {
		t.Fatalf("expected legacy mode diagnostic: %+v", plan.Diagnostics)
	}
	spec, ok := plan.ArtifactForComment("SPEC")
	if !ok {
		t.Fatalf("SPEC artifact not resolved: %+v", plan.Artifacts)
	}
	if !contains(spec.Storage, "SPEC-typed-comment") || !contains(spec.Storage, "durable-archive-output") {
		t.Fatalf("SPEC storage mapping missing active/durable destinations: %+v", spec.Storage)
	}
	task := artifactByID(plan.Artifacts, "tasks")
	if !contains(task.Storage, "PROCESS-typed-comment") || !contains(task.Storage, "issue-spec-links") {
		t.Fatalf("apply.tracks should map to TASK/PROCESS/link state: %+v", task.Storage)
	}
	review := artifactByID(plan.Artifacts, "review")
	if !contains(review.Storage, "REVIEW-typed-comment") || !contains(review.Storage, "pr-review-comment") {
		t.Fatalf("review.md should map to typed review and PR review storage: %+v", review.Storage)
	}
}

func TestResolveBuiltInFallbackWhenNoConfigExists(t *testing.T) {
	root := t.TempDir()
	plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if plan.Source.Kind != SourceBuiltin {
		t.Fatalf("source kind = %q, want builtin", plan.Source.Kind)
	}
	if plan.Source.SchemaName != BuiltinSchemaName {
		t.Fatalf("schema = %q, want builtin", plan.Source.SchemaName)
	}
	if len(plan.Artifacts) == 0 {
		t.Fatal("builtin plan should include artifacts")
	}
}

func TestResolveAcceptsLegacyScalarContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "openspec", "config.yaml"), "schema: issue-spec\ncontext: |\n  Project: existing repository\n")
	plan, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Config.Context["text"] != "Project: existing repository\n" {
		t.Fatalf("context=%#v", plan.Config.Context)
	}
}

func TestWorkflowContextRejectsNonStringScalarsAndAcceptsNull(t *testing.T) {
	for _, raw := range []string{"context: 42\n", "context: true\n"} {
		var cfg Config
		if err := yaml.Unmarshal([]byte(raw), &cfg); err == nil || !strings.Contains(err.Error(), "mapping, string, or null") {
			t.Fatalf("yaml %q error = %v", raw, err)
		}
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte("context: null\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Context != nil {
		t.Fatalf("null context = %#v, want nil", cfg.Context)
	}
}

func TestResolveAcceptsLegacyArtifactRequiresAndInstruction(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "openspec", "config.yaml"), "schema: custom\n")
	writeFile(t, filepath.Join(root, "openspec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: proposal.md
    instruction: Draft the proposal.
    requires: []
  specs:
    type: SPEC
    template: spec.md
    requires:
      - proposal
`)
	writeFile(t, filepath.Join(root, "openspec", "schemas", "custom", "templates", "proposal.md"), "# Proposal\n")
	writeFile(t, filepath.Join(root, "openspec", "schemas", "custom", "templates", "spec.md"), "# Spec\n")

	plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
	if err != nil {
		t.Fatalf("legacy requires/instruction should resolve: %v diagnostics=%+v", err, plan.Diagnostics)
	}
	proposal := artifactByID(plan.Artifacts, "proposal")
	if proposal.Instructions != "Draft the proposal." {
		t.Fatalf("instruction = %q", proposal.Instructions)
	}
	specs := artifactByID(plan.Artifacts, "specs")
	if len(specs.Dependencies) != 1 || specs.Dependencies[0] != "proposal" {
		t.Fatalf("requires should map to dependencies: %+v", specs)
	}
}

func TestExternalCodeConfigAllowsOnlyProviderSelectionAndEvidencePolicy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), `
external_code:
  provider_key: code.example
  evidence:
    sync_before: [verify, runner]
    required: [review, check, merge]
    required_checks: [unit, dco]
    freshness:
      review: 24h
      check: 1h
`)
	plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
	if err != nil {
		t.Fatalf("Resolve returned error: %v diagnostics=%+v", err, plan.Diagnostics)
	}
	if plan.Config.ExternalCode == nil || plan.Config.ExternalCode.ProviderKey != "code.example" ||
		len(plan.Config.ExternalCode.Evidence.RequiredChecks) != 2 ||
		!plan.Config.ExternalCode.Evidence.SynchronizesBefore("verify") ||
		!plan.Config.ExternalCode.Evidence.SynchronizesBefore("RUNNER") {
		t.Fatalf("external code config = %+v", plan.Config.ExternalCode)
	}
}

func TestExternalCodeConfigRejectsInvalidSyncTiming(t *testing.T) {
	for _, timing := range []string{"merge", "verify, verify"} {
		t.Run(timing, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "external_code:\n  provider_key: code.example\n  evidence:\n    sync_before: ["+timing+"]\n")
			plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
			if err == nil || !hasDiagnostic(plan.Diagnostics, "invalid_config") {
				t.Fatalf("sync timing %q should fail: plan=%+v err=%v", timing, plan, err)
			}
		})
	}
}

func TestExternalCodeConfigRejectsRepositoryExecutableAndCredentials(t *testing.T) {
	for _, field := range []string{"executable: /tmp/provider", "args: [--token]", "credentials: TOKEN", "env: [TOKEN=secret]"} {
		t.Run(field[:strings.Index(field, ":")], func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "external_code:\n  provider_key: code.example\n  "+field+"\n")
			plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
			if err == nil || !hasDiagnostic(plan.Diagnostics, "invalid_config") {
				t.Fatalf("operator field %q should fail: plan=%+v err=%v", field, plan, err)
			}
		})
	}
}

func TestResolveRejectsUnsafeAndMissingTemplates(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: ../proposal.md
  design:
    type: design
    template: missing.md
`)

	plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
	if err == nil {
		t.Fatalf("expected validation error, got plan=%+v", plan)
	}
	if !hasDiagnostic(plan.Diagnostics, "unsafe_template_path") {
		t.Fatalf("missing unsafe template diagnostic: %+v", plan.Diagnostics)
	}
	if !hasDiagnostic(plan.Diagnostics, "missing_template") {
		t.Fatalf("missing missing-template diagnostic: %+v", plan.Diagnostics)
	}
}

func TestResolveReportsUnknownArtifactFields(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: proposal.md
    display: Proposal
`)
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "proposal.md"), "# Proposal\n")

	plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
	if err != nil {
		t.Fatalf("non-required unknown field should warn without failing: %v diagnostics=%+v", err, plan.Diagnostics)
	}
	if !hasDiagnostic(plan.Diagnostics, "unknown_artifact_field") {
		t.Fatalf("expected unknown field diagnostic: %+v", plan.Diagnostics)
	}
	proposal := artifactByID(plan.Artifacts, "proposal")
	if !contains(proposal.UnknownFields, "display") {
		t.Fatalf("artifact should preserve unknown fields: %+v", proposal)
	}
}

func TestResolveRejectsRequiredLikeUnknownArtifactFields(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: proposal.md
    required_behavior: repo-local-output
`)
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "proposal.md"), "# Proposal\n")

	plan, err := ResolveWithOptions(ResolveOptions{Root: root, UserConfigDir: filepath.Join(root, "user")})
	if err == nil {
		t.Fatalf("required-like unknown field should fail validation: %+v", plan)
	}
	if !hasDiagnostic(plan.Diagnostics, "unsupported_artifact_field") {
		t.Fatalf("expected unsupported field diagnostic: %+v", plan.Diagnostics)
	}
}

func TestSelectArchivePathPrefersExistingLegacySpec(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "openspec", "specs", "compat", "spec.md"), "# Legacy\n")

	selection := SelectArchivePath(root, "compat", "")
	if selection.Path != filepath.Join("openspec", "specs", "compat", "spec.md") {
		t.Fatalf("path = %q, want legacy openspec path", selection.Path)
	}
	if !selection.Legacy || selection.Source != "legacy-existing" {
		t.Fatalf("selection should report legacy source: %+v", selection)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasDiagnostic(diags []Diagnostic, code string) bool {
	for _, diag := range diags {
		if diag.Code == code {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func artifactByID(artifacts []Artifact, id string) Artifact {
	for _, artifact := range artifacts {
		if artifact.ID == id {
			return artifact
		}
	}
	return Artifact{}
}
