package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/github"
)

func (a *app) runDoctor(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec doctor agent ...\n")
		return 2
	}
	switch args[0] {
	case "agent":
		return a.runDoctorAgent(ctx, args[1:])
	default:
		a.errorf("unknown doctor command %q\n", args[0])
		return 2
	}
}

func (a *app) runDoctorAgent(ctx context.Context, args []string) int {
	fs := newFlagSet("doctor agent", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	jsonOut := fs.Bool("json", false, "write JSON output")
	var rawOperations []string
	fs.Func("operation", "required operation (repeatable)", func(value string) error {
		rawOperations = append(rawOperations, value)
		return nil
	})
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if len(rawOperations) == 0 {
		a.errorf("at least one --operation is required (known: %s)\n", joinCapabilityOperations(capability.KnownOperations()))
		return 2
	}
	operations := make([]capability.Operation, 0, len(rawOperations))
	for _, value := range rawOperations {
		operation, err := capability.ParseOperation(value)
		if err != nil {
			a.errorf("%v (known: %s)\n", err, joinCapabilityOperations(capability.KnownOperations()))
			return 2
		}
		operations = append(operations, operation)
	}
	request := capability.Request{Host: auth.NormalizeHost(*host), Repository: repo, Operations: operations}
	var report capability.Report
	var err error
	if a.doctorAgentProbe != nil {
		report, err = a.doctorAgentProbe(ctx, request)
	} else {
		report, err = a.probeAgentCapabilities(ctx, *host, request)
	}
	if err != nil {
		// Probe implementations may fail while opening a credential source. Keep
		// paths and provider diagnostics out of the user-facing command surface;
		// structured probes should return stable redacted failures in Report.
		a.errorf("agent capability preflight failed\n")
		return 1
	}
	if *jsonOut {
		if code := a.outputJSON(report); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(a.out, "agent capability preflight: ok=%t backend=%s host=%s repo=%s network=%s credential=%s\n",
			report.OK, report.Backend, report.Host, report.Repository, report.Network.Status, report.Credential.SourceClass)
		for _, result := range report.Operations {
			fmt.Fprintf(a.out, "- %s: %s", result.Operation, result.Decision)
			if result.Code != "" {
				fmt.Fprintf(a.out, " (%s)", result.Code)
			}
			if result.Detail != "" {
				fmt.Fprintf(a.out, " — %s", result.Detail)
			}
			fmt.Fprintln(a.out)
		}
	}
	if !report.OK {
		return 1
	}
	return 0
}

func (a *app) probeAgentCapabilities(ctx context.Context, host string, request capability.Request) (capability.Report, error) {
	backend, token, err := a.clientFor(ctx, host)
	if err != nil {
		return capability.FailureReport(request, "", "", "unknown", capability.DecisionDenied,
			capability.FailureAuthenticationFailed, "backend authentication setup failed"), nil
	}
	probeBackend, ok := backend.(github.AgentCapabilityBackend)
	if !ok {
		return capability.Report{}, fmt.Errorf("selected backend does not support read-only agent capability probes")
	}
	profile, _, err := auth.ResolveProfile(a.profileName, host)
	if err != nil {
		return capability.FailureReport(request, token.Source, backend.BackendInfo().Name, "reachable", capability.DecisionDenied,
			capability.FailureInvalidRequest, "backend profile is invalid"), nil
	}
	return github.ProbeAgentCapabilities(ctx, probeBackend, request, github.AgentCapabilityProbeOptions{
		CredentialSource:  token.Source,
		CodeReviewSurface: profile.Kind == auth.ProfileKindGitHub,
	}), nil
}

func joinCapabilityOperations(operations []capability.Operation) string {
	values := make([]string, len(operations))
	for index, operation := range operations {
		values[index] = string(operation)
	}
	return strings.Join(values, ", ")
}
