package commands

import (
	"context"
	"fmt"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func (a *app) runWorkflowUpdate(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow update", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	toolsArg := fs.String("tools", "", "codex, claude, all, or none (default detect)")
	deliveryFlag := fs.String("delivery", "skills", "skills, commands, or both")
	dryRun := fs.Bool("dry-run", false, "preview managed-file changes and conflicts without writing")
	jsonOut := fs.Bool("json", false, "write update plan as JSON")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	delivery, tools, err := resolveWorkflowGenerationOptions(".", *toolsArg, *deliveryFlag)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if len(tools) == 0 {
		a.errorf("no workflow tools selected; use --tools codex,claude\n")
		return 2
	}
	plan, err := workflow.Resolve(".")
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	// Refresh provider guidance using the operator-owned registry, without
	// mutating source bindings, remote labels, Issues or authentication.
	var providerPlan *workflow.ProviderPlan
	if plan.Config.ExternalCode != nil && plan.Config.ExternalCode.ProviderKey != "" {
		profile, _, e := auth.ResolveProfile(a.profileName, "github.com")
		if e != nil {
			a.errorf("%v\n", e)
			return 1
		}
		registry, _, e := codereview.LoadOperatorRegistry(profile.OperatorRegistryFile)
		if e != nil {
			a.errorf("%v\n", e)
			return 1
		}
		key := plan.Config.ExternalCode.ProviderKey
		description, found := findProviderDescription(registry.Descriptions(), key)
		if !found {
			a.errorf("provider %q is not registered\n", key)
			return 1
		}
		provider, e := registry.Lookup(key)
		if e != nil {
			a.errorf("%v\n", e)
			return 1
		}
		capabilities, e := provider.Capabilities(ctx)
		if e != nil {
			a.errorf("%v\n", e)
			return 1
		}
		resolved, e := workflow.NewProviderPlan(description, capabilities)
		if e != nil {
			a.errorf("%v\n", e)
			return 1
		}
		providerPlan = &resolved
	}
	update, err := planWorkflowUpdate(".", repo, delivery, tools, plan, providerPlan)
	if err != nil {
		a.errorf("plan workflow update: %v\n", err)
		return 1
	}
	if !*dryRun && len(update.Conflicts) == 0 {
		err = applyWorkflowUpdate(".", update)
	}
	if err != nil {
		a.errorf("apply workflow update: %v\n", err)
		return 1
	}
	if *jsonOut {
		if code := a.outputJSON(update); code != 0 {
			return code
		}
	} else {
		for _, change := range update.Changes {
			fmt.Fprintf(a.out, "%s %s\n", change.Action, change.Path)
		}
		for _, conflict := range update.Conflicts {
			fmt.Fprintf(a.out, "conflict: %s\n", conflict)
		}
		fmt.Fprintf(a.out, "workflow update: %d changes, %d conflicts, dry_run=%t\n", len(update.Changes), len(update.Conflicts), *dryRun)
	}
	if len(update.Conflicts) > 0 {
		return 1
	}
	return 0
}
