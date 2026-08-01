package assignment

import (
	"errors"
	"fmt"
)

// ValidateReviewReceiptCoverage validates exact required-test coverage in
// addition to the review receipt's verdict and finding structure.
func ValidateReviewReceiptCoverage(required ReviewPayload, receipt Receipt) error {
	if err := validateReview(required, required.SnapshotRevision); err != nil {
		return fmt.Errorf("required review payload: %w", err)
	}
	if err := receipt.ValidateForAcceptance(); err != nil {
		return err
	}
	if receipt.Role != RoleReview || receipt.Review == nil {
		return errors.New("receipt must contain a review result")
	}
	if receipt.SubjectRevision != required.SnapshotRevision {
		return errors.New("review receipt subject revision does not match the exact assigned revision")
	}
	return validateExactTestCoverage("review", required.RequiredTests, receipt.Tests, required.SnapshotRevision)
}

func validateExactTestCoverage(role string, required []TestSelector, actual []TestResult, authoritativeRevision string) error {
	if len(actual) != len(required) {
		return fmt.Errorf("%s receipt tests must exactly cover all %d assigned required tests", role, len(required))
	}
	results := make(map[string]TestResult, len(actual))
	for _, result := range actual {
		results[result.ID] = result
	}
	for _, expected := range required {
		result, ok := results[expected.ID]
		if !ok {
			return fmt.Errorf("%s receipt is missing assigned test %s", role, expected.ID)
		}
		if expected.RevisionBinding == nil {
			if result.AssignedSelector != nil || result.ResolvedRevision != "" || result.Command != expected.Command {
				return fmt.Errorf("%s receipt is missing exact literal assigned test %s command %q", role, expected.ID, expected.Command)
			}
		} else {
			resolved, err := ResolveTestSelector(expected, authoritativeRevision)
			if err != nil {
				return fmt.Errorf("%s assigned test %s: %w", role, expected.ID, err)
			}
			if result.AssignedSelector == nil || !TestSelectorIdentityEqual(*result.AssignedSelector, expected) {
				return fmt.Errorf("%s receipt test %s has a different assigned selector identity", role, expected.ID)
			}
			if result.ResolvedRevision != resolved.ResolvedRevision {
				return fmt.Errorf("%s receipt test %s has a different resolved revision", role, expected.ID)
			}
			if result.Command != resolved.Command {
				return fmt.Errorf("%s receipt test %s has a different executed command", role, expected.ID)
			}
		}
		if result.Outcome != TestPassed {
			return fmt.Errorf("%s test %s must pass before completion", role, result.ID)
		}
	}
	return nil
}
