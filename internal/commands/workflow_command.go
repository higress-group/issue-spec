package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/buildinfo"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/workflow"
)

type workflowCommandResult struct {
	OK       bool          `json:"ok"`
	Repo     string        `json:"repo"`
	Workflow workflow.Plan `json:"workflow"`
	Error    string        `json:"error,omitempty"`
}

func (a *app) runWorkflow(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec workflow validate|which|preflight|reconcile|workspace ...\n")
		return 2
	}
	switch args[0] {
	case "workspace":
		return a.runWorkflowWorkspace(ctx, args[1:])
	case "validate":
		return a.runWorkflowInspect(ctx, args[1:], false)
	case "which":
		return a.runWorkflowInspect(ctx, args[1:], true)
	case "preflight":
		return a.runWorkflowPreflight(ctx, args[1:])
	case "reconcile":
		return a.runWorkflowReconcile(ctx, args[1:])
	default:
		a.errorf("unknown workflow command %q\n", args[0])
		return 2
	}
}

type workflowPreflightProvider struct {
	Key                string                     `json:"key"`
	SemanticGeneration string                     `json:"semantic_generation"`
	ImmutableBuild     string                     `json:"immutable_build"`
	Capabilities       []codereview.Capability    `json:"capabilities"`
	RequiredChecks     []codereview.CheckIdentity `json:"required_checks"`
}

type workflowPreflightResult struct {
	OK                          bool                      `json:"ok"`
	Repository                  string                    `json:"repository"`
	ReleaseSet                  string                    `json:"release_set"`
	CLI                         buildinfo.Info            `json:"cli"`
	ServerRelease               string                    `json:"server_release"`
	RunnerRelease               string                    `json:"runner_release"`
	GeneratedAssets             workflowReleaseManifest   `json:"generated_assets"`
	PinnedGeneratedDigest       string                    `json:"pinned_generated_digest"`
	Provider                    workflowPreflightProvider `json:"provider"`
	PinnedProviderGeneration    string                    `json:"pinned_provider_generation"`
	PinnedProviderBuild         string                    `json:"pinned_provider_build"`
	ReviewMode                  string                    `json:"review_mode"`
	CanonicalPrincipalSource    string                    `json:"canonical_principal_source"`
	ReconciliationMode          string                    `json:"reconciliation_mode"`
	ConditionalMergeEnforcement string                    `json:"conditional_merge_enforcement"`
	ExternalAuthorityMode       string                    `json:"external_authority_mode"`
	Errors                      []string                  `json:"errors,omitempty"`
}

func (a *app) runWorkflowPreflight(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow preflight", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	releaseSet := fs.String("release-set", "", "pinned immutable CLI/Server/Runner/generated release")
	serverRelease := fs.String("server-release", "", "freshly observed Server release identity")
	runnerRelease := fs.String("runner-release", "", "freshly observed Runner release identity")
	manifestPath := fs.String("generated-manifest", "", "generated workflow release manifest path")
	generatedDigest := fs.String("generated-digest", "", "pinned generated workflow content digest")
	providerGeneration := fs.String("provider-generation", codereview.MergeAuthorityGeneration, "pinned provider semantic generation")
	providerBuild := fs.String("provider-build", "", "pinned immutable provider bridge build identity")
	principalSource := fs.String("canonical-principals", "", "operator-owned canonical-principal mapping identity")
	reviewMode := fs.String("review-mode", "provider_native", "provider_native or issue_fallback_required")
	reconciliationMode := fs.String("reconciliation-mode", "post-merge-idempotent", "post-merge reconciliation enforcement")
	enforcementMode := fs.String("enforcement-mode", "provider-authority-token", "conditional-merge enforcement mode")
	externalAuthorityMode := fs.String("external-authority-mode", "disabled", "disabled or provider-atomic-generation")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	result := workflowPreflightResult{Repository: repo, ReleaseSet: strings.TrimSpace(*releaseSet),
		ServerRelease: strings.TrimSpace(*serverRelease), RunnerRelease: strings.TrimSpace(*runnerRelease),
		CLI: buildinfo.Current(), ReviewMode: strings.TrimSpace(*reviewMode),
		PinnedGeneratedDigest:    strings.TrimSpace(*generatedDigest),
		PinnedProviderGeneration: strings.TrimSpace(*providerGeneration), PinnedProviderBuild: strings.TrimSpace(*providerBuild),
		CanonicalPrincipalSource: strings.TrimSpace(*principalSource), ReconciliationMode: strings.TrimSpace(*reconciliationMode),
		ConditionalMergeEnforcement: strings.TrimSpace(*enforcementMode), ExternalAuthorityMode: strings.TrimSpace(*externalAuthorityMode)}
	manifest, err := readWorkflowReleaseManifest(".", *manifestPath)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
	} else {
		result.GeneratedAssets = manifest
	}
	if result.PinnedGeneratedDigest == "" || manifest.ContentDigest != result.PinnedGeneratedDigest {
		result.Errors = append(result.Errors, "generated workflow content digest does not match the pinned release set")
	}
	plan, err := workflow.Resolve(".")
	if err != nil {
		result.Errors = append(result.Errors, "resolve workflow: "+err.Error())
	}
	providerKey, checks, configErr := plan.MergeAuthorityConfiguration()
	if configErr != nil {
		result.Errors = append(result.Errors, configErr.Error())
	}
	result.Provider.Key, result.Provider.RequiredChecks = providerKey, checks
	if result.ReleaseSet == "" || result.ServerRelease == "" || result.RunnerRelease == "" {
		result.Errors = append(result.Errors, "release-set, server-release, and runner-release are required")
	} else if result.ServerRelease != result.ReleaseSet || result.RunnerRelease != result.ReleaseSet ||
		manifest.Release != result.ReleaseSet || result.CLI.Version != result.ReleaseSet || manifest.SourceRevision != result.CLI.Revision ||
		manifest.RequirementsSkillContentID != result.CLI.RequirementsSkillContentID {
		result.Errors = append(result.Errors, "CLI, Server, Runner, and generated assets do not identify one pinned release set")
	}
	if result.CanonicalPrincipalSource == "" {
		result.Errors = append(result.Errors, "canonical-principal mapping identity is required")
	}
	if result.PinnedProviderGeneration != codereview.MergeAuthorityGeneration || result.PinnedProviderBuild == "" {
		result.Errors = append(result.Errors, "pinned provider semantic generation and immutable bridge build are required")
	}
	if result.ReconciliationMode != "post-merge-idempotent" {
		result.Errors = append(result.Errors, "reconciliation mode must be post-merge-idempotent")
	}
	if result.ConditionalMergeEnforcement != "provider-authority-token" {
		result.Errors = append(result.Errors, "conditional merge must enforce the provider authority token")
	}
	if result.ReviewMode != string(codereview.ReviewProviderNative) && result.ReviewMode != string(codereview.ReviewIssueFallbackRequired) {
		result.Errors = append(result.Errors, "review mode is unsupported")
	}
	if result.ReviewMode == string(codereview.ReviewIssueFallbackRequired) && result.ExternalAuthorityMode != "provider-atomic-generation" {
		result.Errors = append(result.Errors, "review fallback requires provider-atomic-generation external authority")
	}
	if result.ReviewMode == string(codereview.ReviewProviderNative) && result.ExternalAuthorityMode != "disabled" {
		result.Errors = append(result.Errors, "provider-native review must not enable external authority fallback")
	}
	fallbackConfigured := plan.Config.ExternalCode != nil && plan.Config.ExternalCode.Merge != nil &&
		plan.Config.ExternalCode.Merge.ReviewFallback != nil && plan.Config.ExternalCode.Merge.ReviewFallback.Enabled
	if fallbackConfigured != (result.ReviewMode == string(codereview.ReviewIssueFallbackRequired)) {
		result.Errors = append(result.Errors, "review mode does not match external_code.merge.review_fallback")
	}
	if providerKey != "" {
		profile, _, profileErr := auth.ResolveProfile(a.profileName, *host)
		if profileErr != nil {
			result.Errors = append(result.Errors, "resolve profile: "+profileErr.Error())
		} else {
			provider, providerErr := a.resolveOperatorProvider(ctx, profile, providerKey)
			if providerErr != nil {
				result.Errors = append(result.Errors, "resolve operator provider: "+providerErr.Error())
			} else if authority, compatible := provider.(codereview.MergeAuthorityProvider); !compatible {
				result.Errors = append(result.Errors, "selected provider does not implement merge authority")
			} else if capabilities, capabilityErr := codereview.RequireMergeAuthorityCapabilities(ctx, authority); capabilityErr != nil {
				result.Errors = append(result.Errors, capabilityErr.Error())
			} else {
				result.Provider.SemanticGeneration = capabilities.SemanticGeneration
				result.Provider.ImmutableBuild = capabilities.ProviderBuildIdentity
				result.Provider.Capabilities = append([]codereview.Capability(nil), capabilities.Values...)
			}
		}
	}
	if result.Provider.SemanticGeneration != "" && (result.Provider.SemanticGeneration != result.PinnedProviderGeneration ||
		result.Provider.ImmutableBuild != result.PinnedProviderBuild) {
		result.Errors = append(result.Errors, "provider semantic generation or immutable bridge build does not match the pinned release set")
	}
	result.OK = len(result.Errors) == 0
	if *jsonOut {
		_ = a.outputJSON(result)
	} else {
		fmt.Fprintf(a.out, "workflow release preflight: ok=%t release=%s generated=%s provider=%s generation=%s build=%s\n",
			result.OK, result.ReleaseSet, result.GeneratedAssets.ContentDigest, result.Provider.Key,
			result.Provider.SemanticGeneration, result.Provider.ImmutableBuild)
		for _, message := range result.Errors {
			fmt.Fprintf(a.out, "- %s\n", message)
		}
	}
	if !result.OK {
		return 1
	}
	return 0
}

func (a *app) runWorkflowInspect(_ context.Context, args []string, which bool) int {
	name := "workflow validate"
	if which {
		name = "workflow which"
	}
	fs := newFlagSet(name, a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	schema := fs.String("schema", "", "schema name override for diagnostics")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if _, ok := a.validateRepo(*repoFlag); !ok {
		return 2
	}
	plan, err := workflow.ResolveWithOptions(workflow.ResolveOptions{Root: ".", Schema: *schema})
	result := workflowCommandResult{
		OK:       err == nil && !plan.HasErrors(),
		Repo:     *repoFlag,
		Workflow: plan,
	}
	if err != nil {
		result.Error = err.Error()
	}
	if *jsonOut {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
		if !result.OK {
			return 1
		}
		return 0
	}
	if which {
		printWorkflowWhich(a.out, result)
	} else {
		printWorkflowValidate(a.out, result)
	}
	if !result.OK {
		return 1
	}
	return 0
}

func printWorkflowValidate(out interface{ Write([]byte) (int, error) }, result workflowCommandResult) {
	if result.OK {
		fmt.Fprintln(out, "workflow validation OK")
	} else {
		fmt.Fprintln(out, "workflow validation failed")
	}
	printWorkflowSummary(out, result.Workflow)
	printWorkflowDiagnostics(out, result.Workflow.Diagnostics)
	if result.Error != "" && !strings.Contains(result.Error, "workflow validation failed") {
		fmt.Fprintf(out, "error: %s\n", result.Error)
	}
}

func printWorkflowWhich(out interface{ Write([]byte) (int, error) }, result workflowCommandResult) {
	printWorkflowSummary(out, result.Workflow)
	printWorkflowDiagnostics(out, result.Workflow.Diagnostics)
}

func printWorkflowSummary(out interface{ Write([]byte) (int, error) }, plan workflow.Plan) {
	fmt.Fprintf(out, "workflow source: %s\n", plan.Source.Kind)
	fmt.Fprintf(out, "schema: %s\n", plan.Source.SchemaName)
	if plan.Source.ConfigPath != "" {
		fmt.Fprintf(out, "config: %s\n", plan.Source.ConfigPath)
	}
	if plan.Source.SchemaPath != "" {
		fmt.Fprintf(out, "schema path: %s\n", plan.Source.SchemaPath)
	}
	if plan.Source.TemplateDir != "" {
		fmt.Fprintf(out, "template dir: %s\n", plan.Source.TemplateDir)
	}
	if len(plan.Artifacts) > 0 {
		fmt.Fprintln(out, "artifacts:")
		for _, artifact := range plan.Artifacts {
			fmt.Fprintf(out, "- %s type=%s", artifact.ID, artifact.Type)
			if artifact.Template != "" {
				fmt.Fprintf(out, " template=%s", artifact.Template)
			}
			if len(artifact.Storage) > 0 {
				fmt.Fprintf(out, " storage=%s", strings.Join(artifact.Storage, ","))
			}
			fmt.Fprintln(out)
		}
	}
}

func printWorkflowDiagnostics(out interface{ Write([]byte) (int, error) }, diagnostics []workflow.Diagnostic) {
	if len(diagnostics) == 0 {
		return
	}
	fmt.Fprintln(out, "diagnostics:")
	for _, diagnostic := range diagnostics {
		target := diagnostic.Path
		if diagnostic.Artifact != "" {
			if target != "" {
				target = diagnostic.Artifact + " " + target
			} else {
				target = diagnostic.Artifact
			}
		}
		if target != "" {
			fmt.Fprintf(out, "- %s %s %s: %s\n", diagnostic.Severity, diagnostic.Code, target, diagnostic.Message)
		} else {
			fmt.Fprintf(out, "- %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
}
