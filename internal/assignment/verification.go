package assignment

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

func (p VerifierPacket) Validate() error {
	if err := validateVerifierGuidance(p.Guidance); err != nil {
		return err
	}
	if err := validateTestSelectors("verifier_packet.required_tests", p.RequiredTests); err != nil {
		return err
	}
	return validateCheckSelectors("verifier_packet.required_checks", p.RequiredChecks)
}

func (p VerificationPayload) RequiredSelectors() RequiredSelectors {
	return RequiredSelectors{
		Tests:  append([]TestSelector(nil), p.RequiredTests...),
		Checks: append([]CheckSelector(nil), p.RequiredChecks...),
	}
}

// MergeRequiredSelectors combines independently resolved requirements without
// changing identity. Exact repeats are idempotent; reusing a test ID for a
// different command fails closed.
func MergeRequiredSelectors(groups ...RequiredSelectors) (RequiredSelectors, error) {
	merged := RequiredSelectors{}
	tests := map[string]string{}
	checks := map[string]struct{}{}
	for groupIndex, group := range groups {
		if err := validateTestSelectors(fmt.Sprintf("required_selector_groups[%d].tests", groupIndex), group.Tests); err != nil {
			return RequiredSelectors{}, err
		}
		if err := validateCheckSelectors(fmt.Sprintf("required_selector_groups[%d].checks", groupIndex), group.Checks); err != nil {
			return RequiredSelectors{}, err
		}
		for _, test := range group.Tests {
			if command, ok := tests[test.ID]; ok {
				if command != test.Command {
					return RequiredSelectors{}, fmt.Errorf("required test selector %q has conflicting commands %q and %q", test.ID, command, test.Command)
				}
				continue
			}
			tests[test.ID] = test.Command
			merged.Tests = append(merged.Tests, test)
		}
		for _, check := range group.Checks {
			key := check.Provider + "\x00" + check.Name
			if _, ok := checks[key]; ok {
				continue
			}
			checks[key] = struct{}{}
			merged.Checks = append(merged.Checks, check)
		}
	}
	sort.Slice(merged.Tests, func(i, j int) bool { return testSelectorLess(merged.Tests[i], merged.Tests[j]) })
	sort.Slice(merged.Checks, func(i, j int) bool { return checkSelectorLess(merged.Checks[i], merged.Checks[j]) })
	return merged, nil
}

// WithVerifierPacket applies resolved project guidance and selectors to a
// verification payload. It is the stable hook used by assignment compilers and
// by deterministic built-in selectors such as repository durable checking.
func (p VerificationPayload) WithVerifierPacket(packet VerifierPacket) (VerificationPayload, error) {
	if err := packet.Validate(); err != nil {
		return VerificationPayload{}, err
	}
	merged, err := MergeRequiredSelectors(p.RequiredSelectors(), RequiredSelectors{
		Tests:  packet.RequiredTests,
		Checks: packet.RequiredChecks,
	})
	if err != nil {
		return VerificationPayload{}, err
	}
	result := p
	result.RequiredTests = merged.Tests
	result.RequiredChecks = merged.Checks
	result.Guidance = cloneVerifierGuidance(p.Guidance)
	if packet.Guidance != nil {
		candidate := cloneVerifierGuidance(packet.Guidance)
		if result.Guidance != nil && !verifierGuidanceEqual(result.Guidance, candidate) {
			return VerificationPayload{}, errors.New("verifier guidance conflicts with the already sealed project guidance")
		}
		result.Guidance = candidate
	}
	if err := validateVerification(result, result.SubjectRevision); err != nil {
		return VerificationPayload{}, err
	}
	return result, nil
}

func verifierGuidanceEqual(left, right *VerifierGuidance) bool {
	left = cloneVerifierGuidance(left)
	right = cloneVerifierGuidance(right)
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if !bytes.Equal(left.Context, right.Context) || !bytes.Equal(left.RulesVerify, right.RulesVerify) || len(left.Instructions) != len(right.Instructions) {
		return false
	}
	for i := range left.Instructions {
		if left.Instructions[i] != right.Instructions[i] {
			return false
		}
	}
	return true
}

// ValidateVerificationReceiptCoverage validates the exact revision-bound test
// and check selector set carried by a role-owned receipt. Provider check outcome
// and assurance still come from the provider observation; verifier prose is not
// considered by this function.
func ValidateVerificationReceiptCoverage(required VerificationPayload, receipt Receipt) error {
	if err := validateVerification(required, required.SubjectRevision); err != nil {
		return fmt.Errorf("required verification payload: %w", err)
	}
	if err := receipt.ValidateForAcceptance(); err != nil {
		return err
	}
	if receipt.Role != RoleVerification || receipt.Verification == nil {
		return errors.New("receipt must contain a verification result")
	}
	if receipt.SubjectRevision != required.SubjectRevision {
		return errors.New("verification receipt subject revision does not match the exact assigned revision")
	}
	if len(receipt.Tests) != len(required.RequiredTests) {
		return fmt.Errorf("verification receipt tests must exactly cover all %d assigned required tests", len(required.RequiredTests))
	}
	tests := make(map[string]TestResult, len(receipt.Tests))
	for _, result := range receipt.Tests {
		tests[result.ID] = result
	}
	for _, expected := range required.RequiredTests {
		result, ok := tests[expected.ID]
		if !ok || result.Command != expected.Command {
			return fmt.Errorf("verification receipt is missing exact assigned test %s command %q", expected.ID, expected.Command)
		}
		if result.Outcome != TestPassed {
			return fmt.Errorf("verification test %s must pass before VERIFY completion", result.ID)
		}
	}
	actualChecks := receipt.Verification.CheckSelectors
	if len(actualChecks) != len(required.RequiredChecks) {
		return fmt.Errorf("verification receipt checks must exactly cover all %d assigned required checks", len(required.RequiredChecks))
	}
	checks := make(map[string]struct{}, len(actualChecks))
	for _, check := range actualChecks {
		checks[check.Provider+"\x00"+check.Name] = struct{}{}
	}
	for _, expected := range required.RequiredChecks {
		if _, ok := checks[expected.Provider+"\x00"+expected.Name]; !ok {
			return fmt.Errorf("verification receipt is missing exact assigned check %s/%s", expected.Provider, expected.Name)
		}
	}
	return nil
}
