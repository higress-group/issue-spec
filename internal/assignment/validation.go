package assignment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const (
	maxIDLength        = 128
	maxShortTextLength = 256
	maxTextLength      = 4096
	maxCommandLength   = 2048
	maxListItems       = 128
	maxResultItems     = 512
)

var (
	stableIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	processIDPattern = regexp.MustCompile(`^PROCESS-[0-9]{3,}$`)
	specIDPattern    = regexp.MustCompile(`^SPEC-[0-9]{3,}$`)
	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (a Assignment) Validate() error {
	return a.validate(true)
}

// ValidateForStorageRead preserves the structural validation of version-1
// assignments while allowing only the one historical pre-D14 shape: an
// implementation or review assignment whose design_context is absent. It is
// intentionally not used by issuance, packet, assignment-file, or role-owned
// submission paths.
func (a Assignment) ValidateForStorageRead() error {
	return a.validate(false)
}

func (a Assignment) validate(requireDesignContext bool) error {
	if a.SchemaVersion != AssignmentSchemaVersion {
		return fmt.Errorf("schema_version: unsupported value %q", a.SchemaVersion)
	}
	if err := validateStableID("assignment_id", a.ID); err != nil {
		return err
	}
	if !validRole(a.Role) {
		return fmt.Errorf("role: unsupported value %q", a.Role)
	}
	if err := validateRepository(a.Repository); err != nil {
		return fmt.Errorf("repository: %w", err)
	}
	if a.Issue <= 0 {
		return errors.New("issue: must be positive")
	}
	if !processIDPattern.MatchString(a.ProcessID) {
		return fmt.Errorf("process_id: invalid value %q", a.ProcessID)
	}
	if err := validateOptionalRevision("base_revision", a.BaseRevision); err != nil {
		return err
	}
	if err := validateOptionalRevision("subject_revision", a.SubjectRevision); err != nil {
		return err
	}
	if err := validateScenarios("scenarios", a.Scenarios, true); err != nil {
		return err
	}
	if err := validateStringList("dependencies", a.Dependencies, maxShortTextLength, false, false); err != nil {
		return err
	}
	if len(a.Handoff) > maxTextLength {
		return fmt.Errorf("handoff: exceeds %d bytes", maxTextLength)
	}
	if !a.Policy.RequireExactRevision {
		return errors.New("policy.require_exact_revision: must be true in version 1")
	}
	if a.Policy.MaxResultItems < 1 || a.Policy.MaxResultItems > maxResultItems {
		return fmt.Errorf("policy.max_result_items: must be between 1 and %d", maxResultItems)
	}
	if a.ResultSchemaVersion != ReceiptSchemaVersion {
		return fmt.Errorf("result_schema_version: unsupported value %q", a.ResultSchemaVersion)
	}

	payloads := 0
	if a.Implementation != nil {
		payloads++
	}
	if a.Review != nil {
		payloads++
	}
	if a.Verification != nil {
		payloads++
	}
	if payloads != 1 {
		return errors.New("role payload: exactly one of implementation, review, or verification is required")
	}
	switch a.Role {
	case RoleImplementation:
		if a.Implementation == nil {
			return errors.New("role payload: implementation role requires implementation")
		}
		if a.BaseRevision == "" || a.SubjectRevision != "" {
			return errors.New("revision identity: implementation requires base_revision and forbids subject_revision")
		}
		if requireDesignContext || a.DesignContext != nil {
			if err := validateDesignContext(a.DesignContext); err != nil {
				return err
			}
		}
		return validateImplementation(*a.Implementation)
	case RoleReview:
		if a.Review == nil {
			return errors.New("role payload: review role requires review")
		}
		if a.SubjectRevision == "" {
			return errors.New("revision identity: review requires subject_revision")
		}
		if requireDesignContext || a.DesignContext != nil {
			if err := validateDesignContext(a.DesignContext); err != nil {
				return err
			}
		}
		return validateReview(*a.Review, a.SubjectRevision)
	case RoleVerification:
		if a.Verification == nil {
			return errors.New("role payload: verification role requires verification")
		}
		if a.SubjectRevision == "" {
			return errors.New("revision identity: verification requires subject_revision")
		}
		if a.DesignContext != nil {
			return errors.New("design_context: verification assignments must not carry implementation design authority")
		}
		return validateVerification(*a.Verification, a.SubjectRevision)
	default:
		panic("validated role was not handled")
	}
}

func validateDesignContext(value *DesignContext) error {
	if value == nil {
		return errors.New("design_context: is required for implementation and review assignments")
	}
	if err := validateRequiredText("design_context.source_url", value.SourceURL, maxTextLength); err != nil {
		return err
	}
	parsedSource, err := url.Parse(value.SourceURL)
	if err != nil || (parsedSource.Scheme != "http" && parsedSource.Scheme != "https") || parsedSource.Host == "" ||
		parsedSource.User != nil || parsedSource.RawQuery != "" || parsedSource.Fragment != "" {
		return errors.New("design_context.source_url: must be an exact canonical HTTP(S) issue URL without credentials, query, or fragment")
	}
	if value.ReadMode != DesignReadModeCompleteIssueBody {
		return fmt.Errorf("design_context.read_mode: must be %q", DesignReadModeCompleteIssueBody)
	}
	if err := validateRequiredText("design_context.invariant", value.Invariant, maxTextLength); err != nil {
		return err
	}
	if err := validateDesignTextList("design_context.applicable_decisions", value.ApplicableDecisions); err != nil {
		return err
	}
	if err := validateRequiredText("design_context.implementation_direction", value.ImplementationDirection, maxTextLength); err != nil {
		return err
	}
	if err := validateDesignTextList("design_context.must_preserve", value.MustPreserve); err != nil {
		return err
	}
	if err := validateDesignTextList("design_context.must_not", value.MustNot); err != nil {
		return err
	}
	if err := validateDesignTextList("design_context.minimum_verification", value.MinimumVerification); err != nil {
		return err
	}
	if value.ConflictPolicy != DesignConflictPolicyAuthoritativeStop {
		return fmt.Errorf("design_context.conflict_policy: must be %q", DesignConflictPolicyAuthoritativeStop)
	}
	return nil
}

func validateDesignTextList(name string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s: at least one item is required", name)
	}
	if len(values) > maxListItems {
		return fmt.Errorf("%s: exceeds %d items", name, maxListItems)
	}
	for i, value := range values {
		if err := validateRequiredText(fmt.Sprintf("%s[%d]", name, i), value, maxTextLength); err != nil {
			return err
		}
	}
	return nil
}

func validateImplementation(p ImplementationPayload) error {
	if err := validateRequiredText("implementation.objective", p.Objective, maxTextLength); err != nil {
		return err
	}
	if err := validateRequiredText("implementation.branch", p.Branch, maxShortTextLength); err != nil {
		return err
	}
	if strings.HasPrefix(p.Branch, "/") || strings.Contains(p.Branch, `\`) {
		return errors.New("implementation.branch: must be portable")
	}
	if err := validateStringList("implementation.write_ownership", p.WriteOwnership, maxShortTextLength, true, true); err != nil {
		return err
	}
	if err := validateStringList("implementation.shared_touchpoints", p.SharedTouchpoints, maxShortTextLength, false, true); err != nil {
		return err
	}
	if !p.Commit.RequireSingleCommit {
		return errors.New("implementation.commit_policy.require_single_commit: must be true in version 1")
	}
	if err := validateGenerators("implementation.generators", p.Generators); err != nil {
		return err
	}
	return validateTestSelectors("implementation.focused_tests", p.FocusedTests)
}

func validateReview(p ReviewPayload, subject string) error {
	if p.SnapshotRevision != subject {
		return errors.New("review.snapshot_revision: must equal subject_revision")
	}
	if err := validateOptionalRevision("review.diff_base_revision", p.DiffBaseRevision); err != nil {
		return err
	}
	if p.DiffBaseRevision == "" {
		return errors.New("review.diff_base_revision: is required")
	}
	if err := validateStringList("review.authors", p.Authors, maxShortTextLength, true, true); err != nil {
		return err
	}
	if err := validateStringList("review.scope", p.Scope, maxShortTextLength, true, true); err != nil {
		return err
	}
	if len(p.KnownTests) > maxListItems {
		return fmt.Errorf("review.known_tests: exceeds %d items", maxListItems)
	}
	seenKnownTests := map[string]int{}
	for i, test := range p.KnownTests {
		if err := validateRequiredText(fmt.Sprintf("review.known_tests[%d].id", i), test.ID, maxIDLength); err != nil {
			return err
		}
		if err := validateRequiredText(fmt.Sprintf("review.known_tests[%d].command", i), test.Command, maxCommandLength); err != nil {
			return err
		}
		if !validTestOutcome(test.Outcome) {
			return fmt.Errorf("review.known_tests[%d].outcome: unsupported value %q", i, test.Outcome)
		}
		if err := recordIdentityKey(seenKnownTests, "review.known_tests", i, test.ID, test.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateVerification(p VerificationPayload, subject string) error {
	if p.SubjectRevision != subject {
		return errors.New("verification.subject_revision: must equal subject_revision")
	}
	if err := validateVerifierGuidance(p.Guidance); err != nil {
		return err
	}
	if len(p.RequiredTests) == 0 && len(p.RequiredChecks) == 0 {
		return errors.New("verification: at least one required test or provider check is required")
	}
	if err := validateTestSelectors("verification.required_tests", p.RequiredTests); err != nil {
		return err
	}
	return validateCheckSelectors("verification.required_checks", p.RequiredChecks)
}

func validateVerifierGuidance(value *VerifierGuidance) error {
	if value == nil {
		return nil
	}
	if len(value.Context) == 0 && len(value.RulesVerify) == 0 && len(value.Instructions) == 0 {
		return errors.New("verifier guidance: at least one declarative field is required")
	}
	if err := validateGuidanceJSON("verifier guidance context", value.Context, true); err != nil {
		return err
	}
	if err := validateGuidanceJSON("verifier guidance rules_verify", value.RulesVerify, false); err != nil {
		return err
	}
	if len(value.Instructions) > maxListItems {
		return fmt.Errorf("verifier guidance instructions: exceeds %d items", maxListItems)
	}
	seen := map[string]int{}
	for i, instruction := range value.Instructions {
		if err := validateRequiredText(fmt.Sprintf("verifier guidance instructions[%d].artifact_id", i), instruction.ArtifactID, maxIDLength); err != nil {
			return err
		}
		if err := validateRequiredText(fmt.Sprintf("verifier guidance instructions[%d].text", i), instruction.Text, maxTextLength); err != nil {
			return err
		}
		if err := recordIdentityKey(seen, "verifier guidance instructions", i, instruction.ArtifactID, instruction.ArtifactID); err != nil {
			return err
		}
	}
	return nil
}

func validateGuidanceJSON(name string, value json.RawMessage, requireObject bool) error {
	if len(value) == 0 {
		return nil
	}
	if len(value) > maxTextLength {
		return fmt.Errorf("%s: exceeds %d bytes", name, maxTextLength)
	}
	if !json.Valid(value) {
		return fmt.Errorf("%s: must be one valid JSON value", name)
	}
	if requireObject {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded map[string]any
		if err := decoder.Decode(&decoded); err != nil || decoded == nil {
			return fmt.Errorf("%s: must be a JSON object", name)
		}
	}
	return nil
}

func (p ProcessInput) Validate() error {
	if p.Empty() {
		return errors.New("assignment input: at least one structured field is required")
	}
	if p.Objective != "" {
		if err := validateRequiredText("objective", p.Objective, maxTextLength); err != nil {
			return err
		}
	}
	if p.DesignContext != nil {
		if err := validateDesignContext(p.DesignContext); err != nil {
			return err
		}
	}
	if err := validateScenarios("scenario_selectors", p.ScenarioSelectors, false); err != nil {
		return err
	}
	if err := validateTestSelectors("required_tests", p.RequiredTests); err != nil {
		return err
	}
	if err := validateCheckSelectors("required_checks", p.RequiredChecks); err != nil {
		return err
	}
	return validateGenerators("generators", p.Generators)
}

func (p ProcessInput) Empty() bool {
	return strings.TrimSpace(p.Objective) == "" && p.DesignContext == nil && len(p.ScenarioSelectors) == 0 && len(p.RequiredTests) == 0 && len(p.RequiredChecks) == 0 && len(p.Generators) == 0 && p.CommitPolicy == nil
}

func (r Receipt) Validate() error {
	if err := validateReceipt(r, true); err != nil {
		return err
	}
	digest, err := ReceiptDigest(r)
	if err != nil {
		return err
	}
	if r.ReceiptDigest != digest {
		return errors.New("receipt_digest: does not match canonical receipt")
	}
	return nil
}

// ValidateForAcceptance applies version-1 assurance policy in addition to
// structural validation. Runtime-attested is reserved for a future trust route
// and unverified imports remain informational.
func (r Receipt) ValidateForAcceptance() error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Provenance.Route != RouteRoleOwned {
		return fmt.Errorf("provenance.route: %q is not accepted in version 1", r.Provenance.Route)
	}
	if r.Provenance.Assurance != AssuranceSelfReported && r.Provenance.Assurance != AssuranceProviderOwned {
		return fmt.Errorf("provenance.assurance: %q is not accepted in version 1", r.Provenance.Assurance)
	}
	for i, result := range r.Tests {
		if result.Assurance != AssuranceSelfReported && result.Assurance != AssuranceProviderOwned {
			return fmt.Errorf("tests[%d].assurance: %q is not accepted in version 1", i, result.Assurance)
		}
	}
	return nil
}

func validateReceipt(r Receipt, requireDigest bool) error {
	if r.SchemaVersion != ReceiptSchemaVersion {
		return fmt.Errorf("schema_version: unsupported value %q", r.SchemaVersion)
	}
	if err := validateStableID("receipt_id", r.ID); err != nil {
		return err
	}
	if requireDigest && !digestPattern.MatchString(r.ReceiptDigest) {
		return errors.New("receipt_digest: must be a lowercase SHA-256 digest")
	}
	if err := validateStableID("assignment_id", r.AssignmentID); err != nil {
		return err
	}
	if !digestPattern.MatchString(r.AssignmentDigest) {
		return errors.New("assignment_digest: must be a lowercase SHA-256 digest")
	}
	if r.AssignmentGeneration == 0 {
		return errors.New("assignment_generation: must be positive")
	}
	if !validRole(r.Role) {
		return fmt.Errorf("role: unsupported value %q", r.Role)
	}
	if r.ResultSchemaVersion != ReceiptSchemaVersion {
		return fmt.Errorf("result_schema_version: unsupported value %q", r.ResultSchemaVersion)
	}
	for _, revision := range []struct {
		name  string
		value string
	}{
		{"base_revision", r.BaseRevision},
		{"result_revision", r.ResultRevision},
		{"subject_revision", r.SubjectRevision},
	} {
		if err := validateOptionalRevision(revision.name, revision.value); err != nil {
			return err
		}
	}
	if len(r.Tests) > maxListItems {
		return fmt.Errorf("tests: exceeds %d items", maxListItems)
	}
	seenTests := map[string]int{}
	for i, result := range r.Tests {
		if err := validateRequiredText(fmt.Sprintf("tests[%d].id", i), result.ID, maxIDLength); err != nil {
			return err
		}
		if err := validateRequiredText(fmt.Sprintf("tests[%d].command", i), result.Command, maxCommandLength); err != nil {
			return err
		}
		if !validTestOutcome(result.Outcome) {
			return fmt.Errorf("tests[%d].outcome: unsupported value %q", i, result.Outcome)
		}
		if !validAssurance(result.Assurance) {
			return fmt.Errorf("tests[%d].assurance: unsupported value %q", i, result.Assurance)
		}
		if err := recordIdentityKey(seenTests, "tests", i, result.ID, result.ID); err != nil {
			return err
		}
	}
	if !validRoute(r.Provenance.Route) {
		return fmt.Errorf("provenance.route: unsupported value %q", r.Provenance.Route)
	}
	if !validAssurance(r.Provenance.Assurance) {
		return fmt.Errorf("provenance.assurance: unsupported value %q", r.Provenance.Assurance)
	}
	for _, identity := range []struct {
		name  string
		value string
	}{
		{"provenance.writer", r.Provenance.Writer},
		{"provenance.subject", r.Provenance.Subject},
		{"provenance.source", r.Provenance.Source},
	} {
		if err := validateRequiredText(identity.name, identity.value, maxShortTextLength); err != nil {
			return err
		}
	}
	payloads := 0
	if r.Implementation != nil {
		payloads++
	}
	if r.Review != nil {
		payloads++
	}
	if r.Verification != nil {
		payloads++
	}
	if payloads != 1 {
		return errors.New("role result: exactly one of implementation, review, or verification is required")
	}
	switch r.Role {
	case RoleImplementation:
		if r.Implementation == nil {
			return errors.New("role result: implementation role requires implementation")
		}
		if r.BaseRevision == "" || r.ResultRevision == "" || r.SubjectRevision != "" {
			return errors.New("revision identity: implementation receipt requires base_revision and result_revision")
		}
		if err := validateStringList("implementation.changed_paths", r.Implementation.ChangedPaths, maxShortTextLength, true, true); err != nil {
			return err
		}
		if err := validateStringList("implementation.decisions", r.Implementation.Decisions, maxTextLength, false, false); err != nil {
			return err
		}
		if err := validateStringList("implementation.risks", r.Implementation.Risks, maxTextLength, false, false); err != nil {
			return err
		}
		if len(r.Implementation.RationaleDraft) > maxTextLength {
			return fmt.Errorf("implementation.rationale_draft: exceeds %d bytes", maxTextLength)
		}
	case RoleReview:
		if r.Review == nil {
			return errors.New("role result: review role requires review")
		}
		if r.SubjectRevision == "" || r.BaseRevision != "" || r.ResultRevision != "" {
			return errors.New("revision identity: review receipt requires only subject_revision")
		}
		if r.Review.Verdict != ReviewApprove && r.Review.Verdict != ReviewChangesRequested {
			return fmt.Errorf("review.verdict: unsupported value %q", r.Review.Verdict)
		}
		if r.Review.Verdict == ReviewApprove && len(r.Review.Findings) > 0 {
			return errors.New("review.findings: approve verdict cannot contain findings")
		}
		if r.Review.Verdict == ReviewChangesRequested && len(r.Review.Findings) == 0 {
			return errors.New("review.findings: changes-requested verdict requires findings")
		}
		if len(r.Review.Findings) > maxListItems {
			return fmt.Errorf("review.findings: exceeds %d items", maxListItems)
		}
		seenFindings := map[string]int{}
		for i, finding := range r.Review.Findings {
			if err := validateFinding(i, finding); err != nil {
				return err
			}
			if err := recordIdentityKey(seenFindings, "review.findings", i, finding.ID, finding.ID); err != nil {
				return err
			}
		}
	case RoleVerification:
		if r.Verification == nil {
			return errors.New("role result: verification role requires verification")
		}
		if r.SubjectRevision == "" || r.BaseRevision != "" || r.ResultRevision != "" {
			return errors.New("revision identity: verification receipt requires only subject_revision")
		}
		if len(r.Tests) == 0 && len(r.Verification.CheckSelectors) == 0 {
			return errors.New("verification: at least one test result or check selector is required")
		}
		if len(r.Verification.Summary) > maxTextLength {
			return fmt.Errorf("verification.summary: exceeds %d bytes", maxTextLength)
		}
		if err := validateCheckSelectors("verification.check_selectors", r.Verification.CheckSelectors); err != nil {
			return err
		}
	}
	return nil
}

func validateFinding(index int, finding Finding) error {
	prefix := fmt.Sprintf("review.findings[%d]", index)
	if err := validateStableID(prefix+".id", finding.ID); err != nil {
		return err
	}
	if !specIDPattern.MatchString(finding.SpecID) {
		return fmt.Errorf("%s.spec_id: invalid value %q", prefix, finding.SpecID)
	}
	if !processIDPattern.MatchString(finding.OwnerProcessID) {
		return fmt.Errorf("%s.owner_process_id: invalid value %q", prefix, finding.OwnerProcessID)
	}
	if err := validatePortablePath(prefix+".path", finding.Path); err != nil {
		return err
	}
	if finding.Side != "LEFT" && finding.Side != "RIGHT" {
		return fmt.Errorf("%s.side: unsupported value %q", prefix, finding.Side)
	}
	if finding.Line < 1 {
		return fmt.Errorf("%s.line: must be positive", prefix)
	}
	if finding.Severity != "P0" && finding.Severity != "P1" && finding.Severity != "P2" && finding.Severity != "P3" {
		return fmt.Errorf("%s.severity: unsupported value %q", prefix, finding.Severity)
	}
	return validateRequiredText(prefix+".message", finding.Message, maxTextLength)
}

func validateScenarios(name string, values []ScenarioRef, required bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("%s: at least one scenario is required", name)
	}
	if len(values) > maxListItems {
		return fmt.Errorf("%s: exceeds %d items", name, maxListItems)
	}
	seen := map[string]struct{}{}
	for i, value := range values {
		if !specIDPattern.MatchString(value.SpecID) {
			return fmt.Errorf("%s[%d].spec_id: invalid value %q", name, i, value.SpecID)
		}
		if err := validateRequiredText(fmt.Sprintf("%s[%d].scenario", name, i), value.Scenario, maxShortTextLength); err != nil {
			return err
		}
		key := value.SpecID + "\x00" + value.Scenario
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s[%d]: duplicate scenario", name, i)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateGenerators(name string, values []GeneratorPolicy) error {
	if len(values) > maxListItems {
		return fmt.Errorf("%s: exceeds %d items", name, maxListItems)
	}
	seen := map[string]int{}
	for i, generator := range values {
		if err := validateRequiredText(fmt.Sprintf("%s[%d].name", name, i), generator.Name, maxIDLength); err != nil {
			return err
		}
		if err := validateRequiredText(fmt.Sprintf("%s[%d].command", name, i), generator.Command, maxCommandLength); err != nil {
			return err
		}
		if len(generator.RequiredOutputs)+len(generator.RequiredOutputGlobs) == 0 {
			return fmt.Errorf("%s[%d]: at least one required output or required output glob is required", name, i)
		}
		if len(generator.RequiredOutputs)+len(generator.RequiredOutputGlobs) > maxListItems {
			return fmt.Errorf("%s[%d]: exceeds %d combined required output items", name, i, maxListItems)
		}
		if err := validateStringList(fmt.Sprintf("%s[%d].required_outputs", name, i), generator.RequiredOutputs, maxShortTextLength, false, true); err != nil {
			return err
		}
		if err := validateStringList(fmt.Sprintf("%s[%d].required_output_globs", name, i), generator.RequiredOutputGlobs, maxShortTextLength, false, false); err != nil {
			return err
		}
		for outputIndex, output := range generator.RequiredOutputGlobs {
			if err := ValidateRequiredOutputPattern(output); err != nil {
				return fmt.Errorf("%s[%d].required_output_globs[%d]: %w", name, i, outputIndex, err)
			}
		}
		if err := recordIdentityKey(seen, name, i, generator.Name, generator.Name); err != nil {
			return err
		}
	}
	return nil
}

func validateTestSelectors(name string, values []TestSelector) error {
	if len(values) > maxListItems {
		return fmt.Errorf("%s: exceeds %d items", name, maxListItems)
	}
	seen := map[string]int{}
	for i, value := range values {
		if err := validateRequiredText(fmt.Sprintf("%s[%d].id", name, i), value.ID, maxIDLength); err != nil {
			return err
		}
		if err := validateRequiredText(fmt.Sprintf("%s[%d].command", name, i), value.Command, maxCommandLength); err != nil {
			return err
		}
		if err := recordIdentityKey(seen, name, i, value.ID, value.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateCheckSelectors(name string, values []CheckSelector) error {
	if len(values) > maxListItems {
		return fmt.Errorf("%s: exceeds %d items", name, maxListItems)
	}
	seen := map[string]int{}
	for i, value := range values {
		if err := validateRequiredText(fmt.Sprintf("%s[%d].provider", name, i), value.Provider, maxIDLength); err != nil {
			return err
		}
		if err := validateRequiredText(fmt.Sprintf("%s[%d].name", name, i), value.Name, maxShortTextLength); err != nil {
			return err
		}
		key := value.Provider + "\x00" + value.Name
		if err := recordIdentityKey(seen, name, i, key, value.Provider+"/"+value.Name); err != nil {
			return err
		}
	}
	return nil
}

func recordIdentityKey(seen map[string]int, list string, index int, key, display string) error {
	if first, ok := seen[key]; ok {
		return fmt.Errorf("%s[%d]: duplicate key %q (first at index %d)", list, index, display, first)
	}
	seen[key] = index
	return nil
}

func validateStringList(name string, values []string, maxLength int, required, paths bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("%s: at least one item is required", name)
	}
	if len(values) > maxListItems {
		return fmt.Errorf("%s: exceeds %d items", name, maxListItems)
	}
	seen := map[string]struct{}{}
	for i, value := range values {
		var err error
		if paths {
			err = validatePortablePath(fmt.Sprintf("%s[%d]", name, i), value)
		} else {
			err = validateRequiredText(fmt.Sprintf("%s[%d]", name, i), value, maxLength)
		}
		if err != nil {
			return err
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s[%d]: duplicate value", name, i)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validatePortablePath(name, value string) error {
	if err := validateRequiredText(name, value, maxShortTextLength); err != nil {
		return err
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") || strings.Contains(value, `\`) || path.Clean(value) == ".." || strings.HasPrefix(path.Clean(value), "../") {
		return fmt.Errorf("%s: must be a portable repository-relative path or pattern", name)
	}
	return nil
}

func validateRepository(value string) error {
	if len(value) == 0 || len(value) > maxShortTextLength || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") || strings.ContainsAny(value, "\\\n\r\t") {
		return errors.New("must be a portable provider/repository identity")
	}
	parts := strings.Split(strings.TrimPrefix(value, "github:"), "/")
	if len(parts) < 2 {
		return errors.New("must include owner and repository")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("contains an invalid component")
		}
	}
	return nil
}

func validateStableID(name, value string) error {
	if !stableIDPattern.MatchString(value) {
		return fmt.Errorf("%s: invalid value %q", name, value)
	}
	return nil
}

func validateRequiredText(name, value string, max int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: is required", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s: must not have surrounding whitespace", name)
	}
	if len(value) > max {
		return fmt.Errorf("%s: exceeds %d bytes", name, max)
	}
	return nil
}

func validateOptionalRevision(name, value string) error {
	if value == "" {
		return nil
	}
	if err := validateRequiredText(name, value, maxShortTextLength); err != nil {
		return err
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("%s: must be an exact opaque revision without whitespace", name)
	}
	return nil
}

func validRole(value Role) bool {
	return value == RoleImplementation || value == RoleReview || value == RoleVerification
}
func validRoute(value ProvenanceRoute) bool {
	return value == RouteRoleOwned || value == RouteUnverifiedImport
}
func validAssurance(value Assurance) bool {
	return value == AssuranceSelfReported || value == AssuranceProviderOwned || value == AssuranceRuntimeAttested
}
func validTestOutcome(value TestOutcome) bool {
	return value == TestPassed || value == TestFailed || value == TestSkipped
}
