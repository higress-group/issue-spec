package processworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
)

var (
	ErrInvalidWorkerResult = errors.New("invalid bounded worker result")
	ErrStaleIntegration    = errors.New("integration HEAD is stale")
	ErrIntegrationConflict = errors.New("worker commit conflicts during integration")
)

type CompleteRequest struct {
	WorkspaceID  string
	OwnerToken   string
	ResultCommit string
	Receipt      *assignment.Receipt
}

type IntegrateRequest struct {
	WorkspaceID  string
	OwnerToken   string
	ExpectedHead string
}

type IntegrationResult struct {
	Lease             LocalLease `json:"lease"`
	IntegrationSHA    string     `json:"integration_sha,omitempty"`
	AlreadyIntegrated bool       `json:"already_integrated"`
}

type integrationRaceHookKey struct{}

type integrationRaceHook func(string) error

const (
	integrationHookBeforeMutation          = "before-mutation"
	integrationHookAfterMutation           = "after-mutation"
	integrationHookBeforeResumePublication = "before-resume-publication"
)

func withIntegrationRaceHook(ctx context.Context, hook integrationRaceHook) context.Context {
	return context.WithValue(ctx, integrationRaceHookKey{}, hook)
}

func runIntegrationRaceHook(ctx context.Context, phase string) error {
	hook, _ := ctx.Value(integrationRaceHookKey{}).(integrationRaceHook)
	if hook == nil {
		return nil
	}
	return hook(phase)
}

// Complete validates a clean one-commit worker result and records
// worker-complete evidence. It never reads changes from a dirty directory.
func (m *Manager) Complete(ctx context.Context, request CompleteRequest) (result Inspection, resultErr error) {
	held, err := m.acquireIntegrationLock(ctx)
	if err != nil {
		return Inspection{}, err
	}
	result, actionErr := m.completeLocked(ctx, request)
	releaseErr := m.releaseIntegrationLock(held)
	return result, errors.Join(actionErr, releaseErr)
}

func (m *Manager) completeLocked(ctx context.Context, request CompleteRequest) (Inspection, error) {
	lease, err := m.ownedLease(ctx, request.WorkspaceID, request.OwnerToken)
	if err != nil {
		return Inspection{}, err
	}
	if err := ValidateManagedOwnership(lease.Portable.WriteOwnership, lease.Portable.SharedTouchpoints); err != nil {
		return Inspection{Lease: lease}, fmt.Errorf("%w: %v", ErrInvalidWorkerResult, err)
	}
	resultCommit := strings.TrimSpace(request.ResultCommit)
	if request.Receipt != nil {
		if resultCommit != "" && !strings.EqualFold(resultCommit, request.Receipt.ResultRevision) {
			return Inspection{Lease: lease}, fmt.Errorf("%w: receipt result revision differs from requested result commit", ErrInvalidWorkerResult)
		}
		resultCommit = strings.TrimSpace(request.Receipt.ResultRevision)
		if err := validateImplementationReceiptBinding(lease, *request.Receipt, resultCommit); err != nil {
			return Inspection{Lease: lease}, fmt.Errorf("%w: %v", ErrInvalidWorkerResult, err)
		}
	}
	if lease.Portable.Mode != ModeWritable {
		return Inspection{Lease: lease}, fmt.Errorf("%w: completion requires writable mode", ErrInvalidWorkerResult)
	}
	if lease.Portable.ResultCommit != "" && !strings.EqualFold(lease.Portable.ResultCommit, resultCommit) {
		return Inspection{Lease: lease}, fmt.Errorf("%w: result commit evidence cannot be replaced", ErrInvalidWorkerResult)
	}
	if lease.Portable.State != StatePrepared && lease.Portable.State != StateWorkerComplete &&
		lease.Portable.State != StateIntegrating && lease.Portable.State != StateIntegrated {
		return Inspection{Lease: lease}, fmt.Errorf("%w: state %s cannot complete a worker result", ErrInvalidWorkerResult, lease.Portable.State)
	}
	inspection, err := m.validateWorkerResult(ctx, lease, resultCommit)
	if err != nil {
		return inspection, err
	}
	if request.Receipt != nil {
		changed, err := m.changedPaths(ctx, lease.WorktreePath, lease.Portable.BaseSHA, resultCommit)
		if err != nil {
			return inspection, err
		}
		if err := m.validateImplementationReceiptContract(ctx, lease, *request.Receipt, resultCommit, changed); err != nil {
			return inspection, fmt.Errorf("%w: %v", ErrInvalidWorkerResult, err)
		}
	}
	if lease.Portable.State != StatePrepared {
		if request.Receipt != nil && lease.Portable.AcceptedReceiptID == "" {
			updated, updateErr := m.Store.Update(ctx, lease.Portable.WorkspaceID, func(current *LocalLease) error {
				if current.Portable.State != lease.Portable.State || !strings.EqualFold(current.Portable.ResultCommit, resultCommit) || current.Portable.AcceptedReceiptID != "" {
					return fmt.Errorf("%w: lease changed during receipt acceptance", ErrWorkspaceConflict)
				}
				persistAcceptedImplementationReceipt(&current.Portable, *request.Receipt)
				current.AcceptedReceiptID = request.Receipt.ID
				return nil
			})
			if updateErr != nil {
				return inspection, updateErr
			}
			inspection.Lease = updated
			return inspection, nil
		}
		inspection.Lease = lease
		return inspection, nil
	}
	updated, err := m.Store.Update(ctx, lease.Portable.WorkspaceID, func(current *LocalLease) error {
		if current.Portable.State != StatePrepared || current.Portable.ResultCommit != "" {
			return fmt.Errorf("%w: lease changed during worker completion", ErrWorkspaceConflict)
		}
		current.Portable.ResultCommit = resultCommit
		current.Portable.State = StateWorkerComplete
		if request.Receipt != nil {
			persistAcceptedImplementationReceipt(&current.Portable, *request.Receipt)
			current.AcceptedReceiptID = request.Receipt.ID
		}
		current.Observation = WorktreeObservation{Registered: true, HeadSHA: resultCommit, Branch: inspection.Branch, Dirty: false, InspectedAt: m.Now().UTC()}
		return nil
	})
	if err != nil {
		return inspection, err
	}
	inspection.Lease = updated
	return inspection, nil
}

func validateImplementationReceiptBinding(lease LocalLease, receipt assignment.Receipt, resultCommit string) error {
	if err := receipt.ValidateForAcceptance(); err != nil {
		return err
	}
	if receipt.Role != assignment.RoleImplementation || receipt.Implementation == nil {
		return errors.New("completion requires an implementation receipt")
	}
	writer := strings.TrimSpace(receipt.Provenance.Writer)
	subject := strings.TrimSpace(receipt.Provenance.Subject)
	if writer == "" || subject == "" || !strings.EqualFold(writer, subject) || strings.EqualFold(writer, "Coordinator") {
		return errors.New("implementation receipt must be owned by one non-Coordinator worker identity")
	}
	binding := lease.Portable.Assignment
	if binding == nil || lease.Assignment == nil {
		return errors.New("result-file completion requires the authoritative persisted assignment binding")
	}
	if receipt.AssignmentID != binding.AssignmentID || receipt.AssignmentDigest != binding.Digest ||
		receipt.AssignmentGeneration != binding.Generation || receipt.Role != binding.Role ||
		receipt.ResultSchemaVersion != lease.Assignment.ResultSchemaVersion {
		return errors.New("receipt does not exactly match the authoritative assignment binding")
	}
	if !strings.EqualFold(receipt.BaseRevision, binding.BaseRevision) || !strings.EqualFold(receipt.BaseRevision, lease.Assignment.BaseRevision) ||
		!strings.EqualFold(receipt.BaseRevision, lease.Portable.BaseSHA) {
		return errors.New("receipt base revision differs from the authoritative assignment")
	}
	if !strings.EqualFold(receipt.ResultRevision, resultCommit) {
		return errors.New("receipt result revision differs from the exact worker result")
	}
	want := acceptedImplementationReceipt(receipt)
	if current := acceptedReceiptBinding(lease.Portable); current != nil && *current != *want {
		return errors.New("accepted receipt identity cannot be replaced")
	}
	if lease.AcceptedReceiptID != "" && lease.AcceptedReceiptID != receipt.ID {
		return errors.New("accepted receipt identity cannot be replaced")
	}
	return nil
}

func acceptedImplementationReceipt(receipt assignment.Receipt) *AcceptedReceiptBinding {
	return &AcceptedReceiptBinding{ReceiptID: receipt.ID, ReceiptDigest: receipt.ReceiptDigest,
		AssignmentGeneration: receipt.AssignmentGeneration}
}

func acceptedReceiptBinding(lease PortableLease) *AcceptedReceiptBinding {
	if lease.AcceptedReceiptID == "" {
		return nil
	}
	return &AcceptedReceiptBinding{ReceiptID: lease.AcceptedReceiptID, ReceiptDigest: lease.AcceptedReceiptDigest,
		AssignmentGeneration: lease.AcceptedReceiptGeneration}
}

func persistAcceptedImplementationReceipt(lease *PortableLease, receipt assignment.Receipt) {
	lease.AcceptedReceiptID = receipt.ID
	lease.AcceptedReceiptDigest = receipt.ReceiptDigest
	lease.AcceptedReceiptGeneration = receipt.AssignmentGeneration
}

func (m *Manager) validateImplementationReceiptContract(ctx context.Context, lease LocalLease, receipt assignment.Receipt, resultCommit string, changed []string) error {
	contract := lease.Assignment.Implementation
	if contract == nil {
		return errors.New("authoritative assignment lacks an implementation contract")
	}
	itemCount := len(receipt.Tests) + len(receipt.Implementation.ChangedPaths) + len(receipt.Implementation.Decisions) + len(receipt.Implementation.Risks)
	if itemCount > lease.Assignment.Policy.MaxResultItems {
		return fmt.Errorf("receipt has %d result items, assignment permits %d", itemCount, lease.Assignment.Policy.MaxResultItems)
	}
	actualPaths := append([]string(nil), changed...)
	reportedPaths := append([]string(nil), receipt.Implementation.ChangedPaths...)
	sort.Strings(actualPaths)
	sort.Strings(reportedPaths)
	if !slices.Equal(actualPaths, reportedPaths) {
		return errors.New("receipt changed_paths differ from the exact Git result")
	}
	tests := make(map[string]assignment.TestResult, len(receipt.Tests))
	for _, result := range receipt.Tests {
		if result.Outcome != assignment.TestPassed {
			return fmt.Errorf("reported test %q did not pass", result.ID)
		}
		tests[result.ID] = result
	}
	for _, required := range contract.FocusedTests {
		result, ok := tests[required.ID]
		if !ok || result.Command != required.Command || result.Outcome != assignment.TestPassed {
			return fmt.Errorf("required focused test %q lacks an exact passing result", required.ID)
		}
	}
	for _, generator := range contract.Generators {
		for _, output := range generator.RequiredOutputs {
			if _, err := m.git(ctx, "validate required generator output", lease.WorktreePath, "cat-file", "-e", resultCommit+":"+output); err != nil {
				return fmt.Errorf("required generator output %q is absent at the result revision: %w", output, err)
			}
		}
	}
	return nil
}

// Integrate cherry-picks exactly one validated worker commit while holding the
// coordinator integration lock. Retried calls recognize a previously applied
// commit from durable lease and Git truth and never cherry-pick it twice.
func (m *Manager) Integrate(ctx context.Context, request IntegrateRequest) (result IntegrationResult, resultErr error) {
	held, err := m.acquireIntegrationLock(ctx)
	if err != nil {
		return IntegrationResult{}, err
	}
	result, actionErr := m.integrateLocked(ctx, request)
	releaseErr := m.releaseIntegrationLock(held)
	return result, errors.Join(actionErr, releaseErr)
}

func (m *Manager) integrateLocked(ctx context.Context, request IntegrateRequest) (IntegrationResult, error) {
	lease, err := m.ownedLease(ctx, request.WorkspaceID, request.OwnerToken)
	if err != nil {
		return IntegrationResult{}, err
	}
	expected := strings.TrimSpace(request.ExpectedHead)
	if !fullSHA.MatchString(expected) {
		return IntegrationResult{Lease: lease}, fmt.Errorf("%w: expected HEAD must be a full Git object id", ErrStaleIntegration)
	}
	if err := ValidateManagedOwnership(lease.Portable.WriteOwnership, lease.Portable.SharedTouchpoints); err != nil {
		return IntegrationResult{Lease: lease}, fmt.Errorf("%w: invalid managed ownership: %v", ErrWorkspaceConflict, err)
	}
	if lease.Portable.State == StateIntegrated {
		if lease.Integration.ExpectedHead == "" || !strings.EqualFold(expected, lease.Integration.ExpectedHead) {
			return IntegrationResult{Lease: lease}, fmt.Errorf("%w: retry expected HEAD differs from recorded integration", ErrStaleIntegration)
		}
		return m.alreadyIntegrated(ctx, lease)
	}
	if lease.Portable.State != StateWorkerComplete && lease.Portable.State != StateIntegrating {
		return IntegrationResult{Lease: lease}, fmt.Errorf("workspace state %s is not integration-ready", lease.Portable.State)
	}
	if lease.Portable.State == StateIntegrating {
		if lease.Integration.ExpectedHead == "" || !strings.EqualFold(expected, lease.Integration.ExpectedHead) {
			return IntegrationResult{Lease: lease}, fmt.Errorf("%w: retry expected HEAD differs from recorded integration", ErrStaleIntegration)
		}
		return m.resumeIntegration(ctx, lease, expected)
	}
	if _, err := m.validateWorkerResult(ctx, lease, lease.Portable.ResultCommit); err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if err := m.validateIntegration(ctx, expected, true); err != nil {
		return IntegrationResult{Lease: lease}, fmt.Errorf("%w: %v", ErrStaleIntegration, err)
	}
	containsBase, err := m.gitPredicate(ctx, m.IntegrationRoot, "merge-base", "--is-ancestor", lease.Portable.BaseSHA, expected)
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if !containsBase {
		return IntegrationResult{Lease: lease}, fmt.Errorf("%w: expected integration HEAD does not contain worker base", ErrStaleIntegration)
	}
	now := m.Now().UTC()
	lease, err = m.Store.Update(ctx, lease.Portable.WorkspaceID, func(current *LocalLease) error {
		if current.Portable.State != StateWorkerComplete || current.Portable.ResultCommit != lease.Portable.ResultCommit {
			return fmt.Errorf("%w: lease changed before integration", ErrWorkspaceConflict)
		}
		current.Portable.State = StateIntegrating
		current.Integration = IntegrationState{ExpectedHead: expected, ObservedHead: expected, StartedAt: now}
		return nil
	})
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	return m.cherryPickIntegrating(ctx, lease, expected)
}

func (m *Manager) resumeIntegration(ctx context.Context, lease LocalLease, expected string) (IntegrationResult, error) {
	if err := m.validateIntegrationCommonAndClean(ctx); err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	head, err := m.gitOutput(ctx, "inspect integration retry HEAD", m.IntegrationRoot, "rev-parse", "HEAD")
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if strings.EqualFold(head, expected) {
		return m.cherryPickIntegrating(ctx, lease, expected)
	}
	markerRef := integrationAttemptRef(lease, expected)
	markerSHA, markerExists, err := m.resolveOptionalRef(ctx, markerRef)
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if !markerExists || (!strings.EqualFold(markerSHA, expected) && !strings.EqualFold(markerSHA, head)) {
		return IntegrationResult{Lease: lease}, fmt.Errorf("%w: integration retry lacks its exact durable attempt marker", ErrStaleIntegration)
	}
	match, err := m.isAppliedWorkerCommit(ctx, expected, lease.Portable.ResultCommit, head)
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if !match {
		return IntegrationResult{Lease: lease}, fmt.Errorf("%w: integration retry HEAD %s is not the recorded worker result", ErrStaleIntegration, head)
	}
	if strings.EqualFold(markerSHA, expected) {
		if err := m.advanceIntegrationAttempt(ctx, markerRef, expected, head); err != nil {
			return IntegrationResult{Lease: lease}, err
		}
	}
	if err := runIntegrationRaceHook(ctx, integrationHookBeforeResumePublication); err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if observed, verifyErr := m.exactCleanIntegrationHead(ctx, head); verifyErr != nil {
		return m.integrationDrift(ctx, lease, markerRef, head, observed,
			fmt.Errorf("%w: recovered integration HEAD changed before lease publication: %v", ErrStaleIntegration, verifyErr))
	}
	updated, err := m.markIntegrated(ctx, lease, head)
	if err != nil {
		return IntegrationResult{Lease: updated}, err
	}
	if observed, verifyErr := m.exactCleanIntegrationHead(ctx, head); verifyErr != nil {
		return m.integrationDrift(ctx, updated, markerRef, head, observed,
			fmt.Errorf("%w: recovered integration HEAD changed during lease publication: %v", ErrStaleIntegration, verifyErr))
	}
	cleanupErr := m.deleteIntegrationAttempt(ctx, markerRef, head)
	return IntegrationResult{Lease: updated, IntegrationSHA: head, AlreadyIntegrated: true}, cleanupErr
}

func (m *Manager) cherryPickIntegrating(ctx context.Context, lease LocalLease, expected string) (IntegrationResult, error) {
	markerRef := integrationAttemptRef(lease, expected)
	if err := m.ensureIntegrationAttempt(ctx, markerRef, expected); err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if err := runIntegrationRaceHook(ctx, integrationHookBeforeMutation); err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	// External Git clients do not share the manager's coordinator lock. Repeat
	// the clean expected-HEAD check immediately before mutation, after the
	// durable attempt marker exists, so a validation-to-cherry-pick drift never
	// becomes the parent of an accepted integration commit.
	if err := m.validateIntegration(ctx, expected, true); err != nil {
		observed, _ := m.gitOutput(ctx, "read pre-mutation drift HEAD", m.IntegrationRoot, "rev-parse", "HEAD")
		return m.integrationDrift(ctx, lease, markerRef, expected, observed, fmt.Errorf("%w: integration changed before cherry-pick: %v", ErrStaleIntegration, err))
	}
	if _, err := m.git(ctx, "cherry-pick bounded worker commit", m.IntegrationRoot, "cherry-pick", lease.Portable.ResultCommit); err != nil {
		abortErr := m.abortCherryPick(ctx, expected)
		var markerErr error
		if abortErr == nil {
			markerErr = m.deleteIntegrationAttempt(ctx, markerRef, expected)
		}
		failure := err.Error()
		updated, updateErr := m.Store.Update(ctx, lease.Portable.WorkspaceID, func(current *LocalLease) error {
			current.Portable.State = StateConflicted
			current.Integration.ObservedHead = expected
			current.Integration.LastError = failure
			return nil
		})
		return IntegrationResult{Lease: updated}, errors.Join(fmt.Errorf("%w: %v", ErrIntegrationConflict, err), abortErr, markerErr, updateErr)
	}
	createdHead, err := m.gitOutput(ctx, "read coordinator cherry-pick HEAD", m.IntegrationRoot, "rev-parse", "HEAD")
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	createdParent, err := m.gitOutput(ctx, "read coordinator cherry-pick parent", m.IntegrationRoot, "rev-parse", createdHead+"^")
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	match, err := m.isAppliedWorkerCommit(ctx, createdParent, lease.Portable.ResultCommit, createdHead)
	if err != nil || !match {
		return IntegrationResult{Lease: lease}, errors.Join(fmt.Errorf("%w: cherry-pick result does not match the bounded worker commit", ErrWorkspaceConflict), err)
	}
	if !strings.EqualFold(createdParent, expected) {
		rollbackErr := m.rollbackCoordinatorCommit(ctx, createdHead, createdParent)
		observed, _ := m.gitOutput(ctx, "read drift HEAD after coordinator rollback", m.IntegrationRoot, "rev-parse", "HEAD")
		recoveryMarker := markerRef
		if rollbackErr != nil {
			// Keep the durable marker when coordinator-created Git state could not
			// be removed; reconciliation still needs that exact attempt evidence.
			recoveryMarker = ""
		}
		result, driftErr := m.integrationDrift(ctx, lease, recoveryMarker, expected, observed,
			fmt.Errorf("%w: cherry-pick parent changed from expected %s to %s", ErrStaleIntegration, expected, createdParent))
		return result, errors.Join(driftErr, rollbackErr)
	}
	if err := runIntegrationRaceHook(ctx, integrationHookAfterMutation); err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	head, err := m.gitOutput(ctx, "read integrated HEAD", m.IntegrationRoot, "rev-parse", "HEAD")
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if !strings.EqualFold(head, createdHead) {
		return m.integrationDrift(ctx, lease, markerRef, expected, head,
			fmt.Errorf("%w: integration HEAD changed after coordinator cherry-pick", ErrStaleIntegration))
	}
	if err := m.advanceIntegrationAttempt(ctx, markerRef, expected, head); err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if observed, verifyErr := m.exactCleanIntegrationHead(ctx, head); verifyErr != nil {
		return m.integrationDrift(ctx, lease, markerRef, head, observed,
			fmt.Errorf("%w: integration HEAD changed before lease publication: %v", ErrStaleIntegration, verifyErr))
	}
	updated, err := m.markIntegrated(ctx, lease, head)
	if err != nil {
		return IntegrationResult{Lease: updated}, err
	}
	if observed, verifyErr := m.exactCleanIntegrationHead(ctx, head); verifyErr != nil {
		return m.integrationDrift(ctx, updated, markerRef, head, observed,
			fmt.Errorf("%w: integration HEAD changed during lease publication: %v", ErrStaleIntegration, verifyErr))
	}
	cleanupErr := m.deleteIntegrationAttempt(ctx, markerRef, head)
	return IntegrationResult{Lease: updated, IntegrationSHA: head}, cleanupErr
}

func (m *Manager) exactCleanIntegrationHead(ctx context.Context, expected string) (string, error) {
	if err := m.validateIntegrationCommonAndClean(ctx); err != nil {
		return "", err
	}
	observed, err := m.gitOutput(ctx, "revalidate exact integration HEAD", m.IntegrationRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(observed, expected) {
		return observed, fmt.Errorf("expected %s, observed %s", expected, observed)
	}
	return observed, nil
}

func (m *Manager) rollbackCoordinatorCommit(ctx context.Context, createdHead, parent string) error {
	if err := m.validateIntegrationCommonAndClean(ctx); err != nil {
		return err
	}
	current, err := m.gitOutput(ctx, "verify coordinator rollback HEAD", m.IntegrationRoot, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if !strings.EqualFold(current, createdHead) {
		return fmt.Errorf("%w: external HEAD moved before coordinator rollback", ErrStaleIntegration)
	}
	if _, err := m.git(ctx, "remove coordinator-created integration commit", m.IntegrationRoot, "update-ref", "HEAD", parent, createdHead); err != nil {
		return err
	}
	// update-ref preserves the exact external commit identity. Reset without an
	// explicit target only refreshes index/worktree to whichever HEAD currently
	// wins; it never rewrites an external commit.
	_, err = m.git(ctx, "refresh integration checkout after coordinator rollback", m.IntegrationRoot, "reset", "--hard")
	return err
}

func (m *Manager) integrationDrift(ctx context.Context, lease LocalLease, markerRef, markerValue, observed string, cause error) (IntegrationResult, error) {
	var markerErr error
	if strings.TrimSpace(markerRef) != "" {
		markerErr = m.deleteIntegrationAttempt(ctx, markerRef, markerValue)
	}
	updated, updateErr := m.Store.Update(ctx, lease.Portable.WorkspaceID, func(current *LocalLease) error {
		if (current.Portable.State != StateIntegrating && current.Portable.State != StateIntegrated) || current.Portable.ResultCommit != lease.Portable.ResultCommit {
			return fmt.Errorf("%w: lease changed while recording integration drift", ErrWorkspaceConflict)
		}
		current.Portable.State = StateConflicted
		current.Portable.IntegrationSHA = ""
		current.Integration.ObservedHead = strings.TrimSpace(observed)
		current.Integration.LastError = cause.Error()
		return nil
	})
	return IntegrationResult{Lease: updated}, errors.Join(cause, markerErr, updateErr)
}

func (m *Manager) abortCherryPick(ctx context.Context, expected string) error {
	if _, err := m.git(ctx, "abort conflicted worker cherry-pick", m.IntegrationRoot, "cherry-pick", "--abort"); err != nil {
		return err
	}
	if err := m.validateIntegration(ctx, expected, true); err != nil {
		return fmt.Errorf("integration was not restored after cherry-pick abort: %w", err)
	}
	return nil
}

func (m *Manager) markIntegrated(ctx context.Context, lease LocalLease, integrationSHA string) (LocalLease, error) {
	now := m.Now().UTC()
	return m.Store.Update(ctx, lease.Portable.WorkspaceID, func(current *LocalLease) error {
		if current.Portable.State != StateIntegrating || current.Portable.ResultCommit != lease.Portable.ResultCommit {
			return fmt.Errorf("%w: lease changed while publishing integration", ErrWorkspaceConflict)
		}
		current.Portable.State = StateIntegrated
		current.Portable.IntegrationSHA = integrationSHA
		current.Integration.ObservedHead = integrationSHA
		current.Integration.CompletedAt = now
		current.Integration.LastError = ""
		return nil
	})
}

func (m *Manager) alreadyIntegrated(ctx context.Context, lease LocalLease) (IntegrationResult, error) {
	if !fullSHA.MatchString(lease.Portable.IntegrationSHA) {
		return IntegrationResult{Lease: lease}, fmt.Errorf("%w: integrated lease lacks integration SHA", ErrWorkspaceConflict)
	}
	if err := m.validateIntegrationCommonAndClean(ctx); err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	contains, err := m.gitPredicate(ctx, m.IntegrationRoot, "merge-base", "--is-ancestor", lease.Portable.IntegrationSHA, "HEAD")
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if !contains {
		return IntegrationResult{Lease: lease}, fmt.Errorf("%w: current integration branch does not contain recorded integration SHA", ErrStaleIntegration)
	}
	markerRef := integrationAttemptRef(lease, lease.Integration.ExpectedHead)
	markerSHA, markerExists, err := m.resolveOptionalRef(ctx, markerRef)
	if err != nil {
		return IntegrationResult{Lease: lease}, err
	}
	if markerExists {
		if !strings.EqualFold(markerSHA, lease.Portable.IntegrationSHA) {
			return IntegrationResult{Lease: lease}, fmt.Errorf("%w: durable integration marker disagrees with published integration", ErrWorkspaceConflict)
		}
		if err := m.deleteIntegrationAttempt(ctx, markerRef, lease.Portable.IntegrationSHA); err != nil {
			return IntegrationResult{Lease: lease}, err
		}
	}
	return IntegrationResult{Lease: lease, IntegrationSHA: lease.Portable.IntegrationSHA, AlreadyIntegrated: true}, nil
}

func (m *Manager) validateWorkerResult(ctx context.Context, lease LocalLease, resultCommit string) (Inspection, error) {
	if !fullSHA.MatchString(resultCommit) {
		return Inspection{Lease: lease}, fmt.Errorf("%w: result commit must be a full Git object id", ErrInvalidWorkerResult)
	}
	resolved, err := m.gitOutput(ctx, "resolve worker result commit", lease.WorktreePath, "rev-parse", "--verify", resultCommit+"^{commit}")
	if err != nil || !strings.EqualFold(resolved, resultCommit) {
		return Inspection{Lease: lease}, errors.Join(fmt.Errorf("%w: result does not resolve to the exact commit", ErrInvalidWorkerResult), err)
	}
	projected := lease
	projected.Portable.State = StateWorkerComplete
	projected.Portable.ResultCommit = resultCommit
	projected.Portable.IntegrationSHA = ""
	inspection, err := m.inspectLeaseAt(ctx, projected, lease.WorktreePath)
	if err != nil {
		return inspection, err
	}
	if !inspection.Registered || !inspection.Present || inspection.Dirty || len(inspection.Problems) > 0 {
		return inspection, fmt.Errorf("%w: worker worktree is not clean, registered, owned, and at result commit: %s", ErrInvalidWorkerResult, strings.Join(inspection.Problems, "; "))
	}
	parents, err := m.gitOutput(ctx, "inspect worker commit parents", lease.WorktreePath, "rev-list", "--parents", "-n", "1", resultCommit)
	if err != nil {
		return inspection, err
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || !strings.EqualFold(fields[0], resultCommit) || !strings.EqualFold(fields[1], lease.Portable.BaseSHA) {
		return inspection, fmt.Errorf("%w: result must be one non-merge commit directly based on reserved base", ErrInvalidWorkerResult)
	}
	countText, err := m.gitOutput(ctx, "count bounded worker commits", lease.WorktreePath, "rev-list", "--count", lease.Portable.BaseSHA+".."+resultCommit)
	if err != nil || countText != "1" {
		return inspection, errors.Join(fmt.Errorf("%w: result must contain exactly one reachable commit, got %q", ErrInvalidWorkerResult, countText), err)
	}
	message, err := m.gitOutput(ctx, "read worker commit message", lease.WorktreePath, "show", "-s", "--format=%B", resultCommit)
	if err != nil {
		return inspection, err
	}
	trailers, err := m.gitInput(ctx, "parse worker commit trailers", lease.WorktreePath, []byte(message+"\n"), "interpret-trailers", "--parse")
	if err != nil || !hasSignedOffTrailer(string(trailers.Stdout)) {
		return inspection, errors.Join(fmt.Errorf("%w: worker commit lacks a valid Signed-off-by trailer", ErrInvalidWorkerResult), err)
	}
	changed, err := m.changedPaths(ctx, lease.WorktreePath, lease.Portable.BaseSHA, resultCommit)
	if err != nil {
		return inspection, err
	}
	if len(changed) == 0 {
		return inspection, fmt.Errorf("%w: empty worker commit", ErrInvalidWorkerResult)
	}
	if err := ValidateManagedWriteScope(lease.Portable.WriteOwnership, lease.Portable.SharedTouchpoints, changed); err != nil {
		return inspection, err
	}
	return inspection, nil
}

func (m *Manager) changedPaths(ctx context.Context, directory, base, result string) ([]string, error) {
	gitResult, err := m.git(ctx, "list bounded worker paths", directory, "diff", "--name-only", "--no-renames", "-z", base, result, "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, value := range bytes.Split(gitResult.Stdout, []byte{0}) {
		if len(value) > 0 {
			paths = append(paths, string(value))
		}
	}
	return paths, nil
}

func (m *Manager) isAppliedWorkerCommit(ctx context.Context, expected, result, observed string) (bool, error) {
	parents, err := m.gitOutput(ctx, "inspect integrated commit parent", m.IntegrationRoot, "rev-list", "--parents", "-n", "1", observed)
	if err != nil {
		return false, err
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || !strings.EqualFold(fields[1], expected) {
		return false, nil
	}
	resultPatch, err := m.commitPatchID(ctx, result)
	if err != nil {
		return false, err
	}
	observedPatch, err := m.commitPatchID(ctx, observed)
	if err != nil || resultPatch != observedPatch {
		return false, err
	}
	resultEvidence, err := m.integrationCommitEvidence(ctx, result, result+"^")
	if err != nil {
		return false, err
	}
	observedEvidence, err := m.integrationCommitEvidence(ctx, observed, expected)
	if err != nil {
		return false, err
	}
	return bytes.Equal(resultEvidence.treeDelta, observedEvidence.treeDelta) &&
		bytes.Equal(resultEvidence.message, observedEvidence.message) &&
		bytes.Equal(resultEvidence.author, observedEvidence.author), nil
}

type integrationEvidence struct {
	treeDelta []byte
	message   []byte
	author    []byte
}

func (m *Manager) integrationCommitEvidence(ctx context.Context, commit, parent string) (integrationEvidence, error) {
	delta, err := m.git(ctx, "read integration tree delta", m.IntegrationRoot, "diff", "--raw", "--no-renames", "-z", parent, commit, "--")
	if err != nil {
		return integrationEvidence{}, err
	}
	message, err := m.git(ctx, "read integration commit message", m.IntegrationRoot, "show", "-s", "--format=%B", commit)
	if err != nil {
		return integrationEvidence{}, err
	}
	author, err := m.git(ctx, "read integration commit author", m.IntegrationRoot, "show", "-s", "--format=%an%x00%ae%x00%at", commit)
	if err != nil {
		return integrationEvidence{}, err
	}
	return integrationEvidence{treeDelta: delta.Stdout, message: message.Stdout, author: author.Stdout}, nil
}

func integrationAttemptRef(lease LocalLease, expected string) string {
	payload := lease.Portable.WorkspaceID + "\x00" + strings.ToLower(expected) + "\x00" + strings.ToLower(lease.Portable.ResultCommit)
	digest := sha256.Sum256([]byte(payload))
	return "refs/issue-spec/process-integrations/" + lease.Portable.WorkspaceID + "/" + hex.EncodeToString(digest[:])
}

func (m *Manager) ensureIntegrationAttempt(ctx context.Context, markerRef, expected string) error {
	markerSHA, exists, err := m.resolveOptionalRef(ctx, markerRef)
	if err != nil {
		return err
	}
	if exists {
		if strings.EqualFold(markerSHA, expected) {
			return nil
		}
		return fmt.Errorf("%w: durable integration marker already advanced to %s", ErrWorkspaceConflict, markerSHA)
	}
	zero := strings.Repeat("0", len(expected))
	if _, err := m.git(ctx, "create durable integration attempt", m.IntegrationRoot, "update-ref", markerRef, expected, zero); err != nil {
		return err
	}
	return nil
}

func (m *Manager) advanceIntegrationAttempt(ctx context.Context, markerRef, expected, integrated string) error {
	markerSHA, exists, err := m.resolveOptionalRef(ctx, markerRef)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: durable integration marker disappeared", ErrWorkspaceConflict)
	}
	if strings.EqualFold(markerSHA, integrated) {
		return nil
	}
	if !strings.EqualFold(markerSHA, expected) {
		return fmt.Errorf("%w: durable integration marker changed from expected HEAD", ErrWorkspaceConflict)
	}
	_, err = m.git(ctx, "advance durable integration attempt", m.IntegrationRoot, "update-ref", markerRef, integrated, expected)
	return err
}

func (m *Manager) deleteIntegrationAttempt(ctx context.Context, markerRef, expected string) error {
	_, err := m.git(ctx, "delete durable integration attempt", m.IntegrationRoot, "update-ref", "-d", markerRef, expected)
	return err
}

func (m *Manager) commitPatchID(ctx context.Context, commit string) (string, error) {
	patch, err := m.git(ctx, "render commit patch", m.IntegrationRoot, "show", "--pretty=format:", "--no-ext-diff", "--binary", commit, "--")
	if err != nil {
		return "", err
	}
	result, err := m.gitInput(ctx, "compute stable commit patch id", m.IntegrationRoot, patch.Stdout, "patch-id", "--stable")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(result.Stdout))
	if len(fields) < 1 || fields[0] == "" {
		return "", errors.New("Git did not produce a patch id")
	}
	return fields[0], nil
}

func (m *Manager) validateIntegrationCommonAndClean(ctx context.Context) error {
	common, err := m.resolveCommonDir(ctx, m.IntegrationRoot)
	if err != nil {
		return err
	}
	if common != m.CommonDir {
		return fmt.Errorf("%w: integration common dir changed", ErrWorkspaceConflict)
	}
	status, err := m.gitOutput(ctx, "inspect integration status", m.IntegrationRoot, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%w: integration checkout is dirty", ErrWorkspaceDirty)
	}
	return nil
}

func (m *Manager) gitPredicate(ctx context.Context, directory string, args ...string) (bool, error) {
	result, err := m.Runner.Run(ctx, GitCommand{Binary: m.GitBinary, Dir: directory, Args: append([]string(nil), args...)})
	if err == nil {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	return false, &GitError{Operation: "evaluate Git predicate", Args: args, Stderr: string(result.Stderr), Err: err}
}

func (m *Manager) ownedLease(ctx context.Context, workspaceID, ownerToken string) (LocalLease, error) {
	lease, found, err := m.Store.Get(ctx, workspaceID)
	if err != nil {
		return LocalLease{}, err
	}
	if !found {
		return LocalLease{}, fmt.Errorf("%s: %w", workspaceID, ErrLeaseNotFound)
	}
	if ownerToken == "" || ownerToken != lease.Owner.Token {
		return LocalLease{}, errors.New("lease owner token mismatch")
	}
	return lease, nil
}

func hasSignedOffTrailer(trailers string) bool {
	for _, line := range strings.Split(trailers, "\n") {
		key, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(key), "Signed-off-by") {
			value = strings.TrimSpace(value)
			open := strings.LastIndex(value, "<")
			if open > 0 && strings.HasSuffix(value, ">") && strings.Contains(value[open+1:len(value)-1], "@") {
				return true
			}
		}
	}
	return false
}
