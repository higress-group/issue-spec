package capability

import (
	"reflect"
	"testing"
)

func TestOperationsAndRequestValidation(t *testing.T) {
	for _, operation := range KnownOperations() {
		parsed, err := ParseOperation(string(operation))
		if err != nil || parsed != operation {
			t.Fatalf("ParseOperation(%q) = %q, %v", operation, parsed, err)
		}
	}
	if _, err := ParseOperation("repo.destroy"); err == nil {
		t.Fatal("unknown operation accepted")
	}
	request := Request{Host: "github.com", Repository: "o/r", Operations: []Operation{OperationIssueRead}}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	request.Repository = "o/r/extra"
	if err := request.Validate(); err == nil {
		t.Fatal("invalid repository accepted")
	}
}

func TestSourceClassNeverReturnsSourceDetail(t *testing.T) {
	got := []string{SourceClass("env:ISSUE_SPEC_TOKEN"), SourceClass("file:/private/token"), SourceClass("gh")}
	want := []string{"environment", "private-file", "external-cli"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source classes = %v, want %v", got, want)
	}
}

func TestReportFinishRequiresEveryOperationAllowed(t *testing.T) {
	report := Report{Operations: []OperationResult{{Operation: OperationIssueRead, Decision: DecisionAllowed},
		{Operation: OperationGitPush, Decision: DecisionUnknown}}}
	report.Finish()
	if report.OK {
		t.Fatal("report passed an unknown operation")
	}
}

func TestFailureReportIsSortedAndRedacted(t *testing.T) {
	report := FailureReport(Request{Host: "github.com", Repository: "o/r", Operations: []Operation{
		OperationGitPush, OperationIssueRead, OperationGitPush,
	}}, "file:/private/secret", "rest", "", DecisionDenied, FailureAuthenticationFailed, "authentication failed")
	if report.OK || report.Credential.SourceClass != "private-file" || len(report.Operations) != 2 ||
		report.Operations[0].Operation != OperationGitPush || report.Operations[1].Operation != OperationIssueRead {
		t.Fatalf("report = %+v", report)
	}
}
