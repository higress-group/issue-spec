package delegation

import (
	"errors"
	"testing"

	"github.com/higress-group/issue-spec/internal/capability"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
)

func TestValidateDelegatedOperationsEnforcesScopeAndSurface(t *testing.T) {
	operations, err := validateDelegatedOperations([]capability.Operation{
		capability.OperationIssueRead, capability.OperationArtifactWrite,
	}, []string{"issues:read", "issues:write"})
	if err != nil || len(operations) != 2 || operations[0] != "artifact.write" || operations[1] != "issue.read" {
		t.Fatalf("operations=%v err=%v", operations, err)
	}
	if _, err := validateDelegatedOperations([]capability.Operation{capability.OperationIssueCommentWrite}, []string{"issues:read"}); !errors.Is(err, serverauth.ErrInsufficientScope) {
		t.Fatalf("write with read scope err=%v", err)
	}
	if _, err := validateDelegatedOperations([]capability.Operation{capability.OperationGitPush}, []string{"issues:write"}); !errors.Is(err, serverauth.ErrInsufficientScope) {
		t.Fatalf("git operation entered issue credential claims err=%v", err)
	}
}
