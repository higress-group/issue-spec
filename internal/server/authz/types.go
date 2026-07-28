// Package authz evaluates tenant-scoped permissions independently from HTTP
// presentation. Callers must provide both organization and repository IDs.
package authz

import (
	"errors"
	"net/http"

	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
)

// Permission is ordered from least to most privileged. The order is part of
// the evaluator contract and lets organization and repository grants compose
// by taking their maximum before credential scopes cap the result.
type Permission uint8

const (
	PermissionNone Permission = iota
	PermissionRead
	PermissionTriage
	PermissionWrite
	PermissionMaintain
	PermissionAdmin
)

func (p Permission) String() string {
	switch p {
	case PermissionRead:
		return "read"
	case PermissionTriage:
		return "triage"
	case PermissionWrite:
		return "write"
	case PermissionMaintain:
		return "maintain"
	case PermissionAdmin:
		return "admin"
	default:
		return "none"
	}
}

// Operation names the stable authorization boundary used by API handlers and
// background processors. Runner policy is deliberately a separate successor
// gate; evidence publishing is fully gated here.
type Operation string

const (
	OperationRead               Operation = "read"
	OperationContribute         Operation = "contribute"
	OperationTriage             Operation = "triage"
	OperationWrite              Operation = "write"
	OperationTriggerRunner      Operation = "runner.trigger"
	OperationPublishEvidence    Operation = "evidence.publish"
	OperationManageIntegrations Operation = "integrations.manage"
	OperationAdminRepository    Operation = "repository.admin"
	OperationReadOrganization   Operation = "organization.read"
	OperationAdminOrganization  Operation = "organization.admin"
)

// Subject is either anonymous or carries an already authenticated principal.
// A present but disabled principal never degrades to anonymous access.
type Subject struct {
	Principal *serverauth.Principal
}

func Anonymous() Subject { return Subject{} }

func Authenticated(principal serverauth.Principal) Subject {
	return Subject{Principal: &principal}
}

// RepositoryRequest contains the complete tenant scope and operation.
type RepositoryRequest struct {
	Scope     models.RepoScope
	Operation Operation
}

// Reason is safe for metrics and tests. It must not be written directly to a
// compatibility response because invisible and absent resources are concealed.
type Reason string

const (
	ReasonAllowed                Reason = "allowed"
	ReasonNotFound               Reason = "not_found"
	ReasonInvisible              Reason = "invisible"
	ReasonInactiveIdentity       Reason = "inactive_identity"
	ReasonInsufficientPermission Reason = "insufficient_permission"
	ReasonCredentialScope        Reason = "credential_scope"
	ReasonRepositoryCap          Reason = "repository_cap"
	ReasonContributionPolicy     Reason = "contribution_policy"
	ReasonUnsupportedOperation   Reason = "unsupported_operation"
)

// Decision keeps existence and visibility separate so adapters can implement
// concealment without losing useful internal diagnostics.
type Decision struct {
	Exists               bool
	Visible              bool
	Allowed              bool
	EffectivePermission  Permission
	RequiredPermission   Permission
	Reason               Reason
	RequiresRunnerPolicy bool
}

// AuthorizationError adapts a decision to the administration service contract.
func (d Decision) AuthorizationError() error {
	if d.Allowed {
		return nil
	}
	if !d.Exists || !d.Visible {
		return adminservice.ErrNotFound
	}
	return adminservice.ErrForbidden
}

// CompatibilityError conceals missing and invisible resources with the same
// GitHub-compatible 404 envelope. Visible denials remain 403.
func (d Decision) CompatibilityError(requestID string) (apierrors.GitHubError, bool) {
	if d.Allowed {
		return apierrors.GitHubError{}, false
	}
	if !d.Exists || !d.Visible {
		return apierrors.NotFound(requestID), true
	}
	return apierrors.Forbidden(requestID), true
}

// NativeProblem uses stable problem codes and the same concealment boundary as
// the compatibility adapter.
func (d Decision) NativeProblem(requestID string) (apierrors.Problem, bool) {
	if d.Allowed {
		return apierrors.Problem{}, false
	}
	if !d.Exists || !d.Visible {
		return apierrors.NewProblem(http.StatusNotFound, "not_found", "Not found", "", requestID), true
	}
	return apierrors.NewProblem(http.StatusForbidden, "forbidden", "Forbidden", "", requestID), true
}

func permissionFromBase(value models.BasePermission) Permission {
	switch value {
	case models.BasePermissionRead:
		return PermissionRead
	case models.BasePermissionTriage:
		return PermissionTriage
	case models.BasePermissionWrite:
		return PermissionWrite
	case models.BasePermissionMaintain:
		return PermissionMaintain
	case models.BasePermissionAdmin:
		return PermissionAdmin
	default:
		return PermissionNone
	}
}

func permissionFromRole(role string) Permission {
	switch role {
	case "owner", "admin":
		return PermissionAdmin
	case "maintainer", "maintain":
		return PermissionMaintain
	case "member", "write":
		return PermissionWrite
	case "triage":
		return PermissionTriage
	case "reader", "read":
		return PermissionRead
	default:
		return PermissionNone
	}
}

func maxPermission(values ...Permission) Permission {
	result := PermissionNone
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}

func minPermission(left, right Permission) Permission {
	if left < right {
		return left
	}
	return right
}

func knownOperation(operation Operation) bool {
	switch operation {
	case OperationRead, OperationContribute, OperationTriage, OperationWrite,
		OperationTriggerRunner, OperationPublishEvidence, OperationManageIntegrations,
		OperationAdminRepository, OperationReadOrganization, OperationAdminOrganization:
		return true
	default:
		return false
	}
}

var errInvalidService = errors.New("authz: database is required")
