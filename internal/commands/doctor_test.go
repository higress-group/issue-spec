package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/capability"
)

func TestDoctorAgentParsesOperationsAndReturnsProbeReport(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.doctorAgentProbe = func(_ context.Context, request capability.Request) (capability.Report, error) {
		if request.Repository != "o/r" || len(request.Operations) != 2 {
			t.Fatalf("request = %+v", request)
		}
		report := capability.Report{Host: request.Host, Repository: request.Repository, Backend: "test",
			Credential: capability.CredentialSummary{SourceClass: "delegated"},
			Network:    capability.NetworkSummary{Status: "reachable"},
			Operations: []capability.OperationResult{{Operation: capability.OperationIssueRead, Decision: capability.DecisionAllowed},
				{Operation: capability.OperationPullRequestRead, Decision: capability.DecisionAllowed}}}
		report.Finish()
		return report, nil
	}
	code := app.runDoctor(context.Background(), []string{"agent", "--repo", "o/r", "--operation", "issue.read", "--operation=pr.read", "--json"})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	var report capability.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v output=%s", report, err, out.String())
	}
}

func TestDoctorAgentFailsClosedForUnknownCapability(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.doctorAgentProbe = func(_ context.Context, request capability.Request) (capability.Report, error) {
		report := capability.Report{Host: request.Host, Repository: request.Repository,
			Credential: capability.CredentialSummary{SourceClass: "external-cli"},
			Network:    capability.NetworkSummary{Status: "reachable"},
			Operations: []capability.OperationResult{{Operation: request.Operations[0], Decision: capability.DecisionUnknown,
				Code: capability.FailureOperationNotProvable}}}
		report.Finish()
		return report, nil
	}
	code := app.runDoctor(context.Background(), []string{"agent", "--repo", "o/r", "--operation", "pr.review.write", "--json"})
	if code != 1 || !strings.Contains(out.String(), `"decision": "unknown"`) || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}

func TestDoctorAgentRejectsMissingAndUnknownOperations(t *testing.T) {
	for _, args := range [][]string{{"agent", "--repo", "o/r"}, {"agent", "--repo", "o/r", "--operation", "repo.destroy"}} {
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		if code := app.runDoctor(context.Background(), args); code != 2 || errOut.Len() == 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, errOut.String())
		}
	}
}

func TestDoctorAgentDoesNotPrintProbeCredentialPath(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.doctorAgentProbe = func(context.Context, capability.Request) (capability.Report, error) {
		return capability.Report{}, errors.New("open /private/tmp/secret-token: permission denied")
	}
	code := app.runDoctor(context.Background(), []string{"agent", "--repo", "o/r", "--operation", "issue.read"})
	if code != 1 || strings.Contains(errOut.String(), "/private/") || strings.Contains(errOut.String(), "secret-token") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}
