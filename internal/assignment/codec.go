package assignment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

func ParseAssignmentJSON(data []byte) (Assignment, error) {
	var value Assignment
	if err := decodeStrict(data, &value); err != nil {
		return Assignment{}, fmt.Errorf("parse assignment: %w", err)
	}
	value = normalizeAssignment(value)
	if err := value.Validate(); err != nil {
		return Assignment{}, fmt.Errorf("validate assignment: %w", err)
	}
	return value, nil
}

func CanonicalAssignmentJSON(value Assignment) ([]byte, error) {
	value = normalizeAssignment(value)
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func AssignmentDigest(value Assignment) (string, error) {
	payload, err := CanonicalAssignmentJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// AssignmentDigestForStorageRead computes the original portable digest for
// persisted version-1 assignments, including the pre-D14 shape that omitted
// design_context. Callers must not use it to issue, redispatch, accept an
// assignment file, or validate a new role submission.
func AssignmentDigestForStorageRead(value Assignment) (string, error) {
	value = normalizeAssignment(value)
	if err := value.ValidateForStorageRead(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (p Packet) Validate() error {
	digest, err := AssignmentDigest(p.Assignment)
	if err != nil {
		return fmt.Errorf("assignment: %w", err)
	}
	if p.AssignmentDigest != digest {
		return errors.New("assignment_digest: does not match the portable assignment")
	}
	if p.Generation == 0 {
		return errors.New("generation: must be positive")
	}
	if p.Delivery != nil && len(p.Delivery.WorktreePath) > maxTextLength {
		return fmt.Errorf("delivery.worktree_path: exceeds %d bytes", maxTextLength)
	}
	return nil
}

func ParsePacketJSON(data []byte) (Packet, error) {
	var value Packet
	if err := decodeStrict(data, &value); err != nil {
		return Packet{}, fmt.Errorf("parse assignment packet: %w", err)
	}
	value.Assignment = normalizeAssignment(value.Assignment)
	if err := value.Validate(); err != nil {
		return Packet{}, fmt.Errorf("validate assignment packet: %w", err)
	}
	return value, nil
}

func ParseProcessInputJSON(data []byte) (ProcessInput, error) {
	var value ProcessInput
	if err := decodeStrict(data, &value); err != nil {
		return ProcessInput{}, fmt.Errorf("parse PROCESS assignment input: %w", err)
	}
	value = normalizeProcessInput(value)
	if err := value.Validate(); err != nil {
		return ProcessInput{}, fmt.Errorf("validate PROCESS assignment input: %w", err)
	}
	return value, nil
}

func ProcessInputJSON(value ProcessInput) ([]byte, error) {
	value = normalizeProcessInput(value)
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(value, "", "  ")
}

func ParseReceiptJSON(data []byte) (Receipt, error) {
	var value Receipt
	if err := decodeStrict(data, &value); err != nil {
		return Receipt{}, fmt.Errorf("parse receipt: %w", err)
	}
	value = normalizeReceipt(value)
	if err := value.Validate(); err != nil {
		return Receipt{}, fmt.Errorf("validate receipt: %w", err)
	}
	digest, err := ReceiptDigest(value)
	if err != nil {
		return Receipt{}, fmt.Errorf("digest receipt: %w", err)
	}
	if value.ReceiptDigest != digest {
		return Receipt{}, errors.New("validate receipt: receipt_digest does not match canonical receipt")
	}
	return value, nil
}

func CanonicalReceiptJSON(value Receipt) ([]byte, error) {
	value = normalizeReceipt(value)
	value.ReceiptDigest = ""
	if err := validateReceipt(value, false); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func ReceiptDigest(value Receipt) (string, error) {
	payload, err := CanonicalReceiptJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func SealReceipt(value Receipt) (Receipt, error) {
	value = normalizeReceipt(value)
	digest, err := ReceiptDigest(value)
	if err != nil {
		return Receipt{}, err
	}
	value.ReceiptDigest = digest
	if err := value.Validate(); err != nil {
		return Receipt{}, err
	}
	return value, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func normalizeAssignment(value Assignment) Assignment {
	clone := value
	clone.DesignContext = cloneDesignContext(value.DesignContext)
	clone.Scenarios = append([]ScenarioRef(nil), value.Scenarios...)
	sort.Slice(clone.Scenarios, func(i, j int) bool {
		if clone.Scenarios[i].SpecID == clone.Scenarios[j].SpecID {
			return clone.Scenarios[i].Scenario < clone.Scenarios[j].Scenario
		}
		return clone.Scenarios[i].SpecID < clone.Scenarios[j].SpecID
	})
	clone.Dependencies = sortedCopy(value.Dependencies)
	if value.Implementation != nil {
		payload := *value.Implementation
		payload.WriteOwnership = sortedCopy(payload.WriteOwnership)
		payload.SharedTouchpoints = sortedCopy(payload.SharedTouchpoints)
		payload.Generators = append([]GeneratorPolicy(nil), payload.Generators...)
		for i := range payload.Generators {
			payload.Generators[i].RequiredOutputs = sortedCopy(payload.Generators[i].RequiredOutputs)
		}
		sort.Slice(payload.Generators, func(i, j int) bool { return payload.Generators[i].Name < payload.Generators[j].Name })
		payload.FocusedTests = append([]TestSelector(nil), payload.FocusedTests...)
		sort.Slice(payload.FocusedTests, func(i, j int) bool { return testSelectorLess(payload.FocusedTests[i], payload.FocusedTests[j]) })
		clone.Implementation = &payload
	}
	if value.Review != nil {
		payload := *value.Review
		payload.Authors = sortedCopy(payload.Authors)
		payload.Scope = sortedCopy(payload.Scope)
		payload.KnownTests = append([]KnownTestEvidence(nil), payload.KnownTests...)
		sort.Slice(payload.KnownTests, func(i, j int) bool { return payload.KnownTests[i].ID < payload.KnownTests[j].ID })
		clone.Review = &payload
	}
	if value.Verification != nil {
		payload := *value.Verification
		payload.Guidance = cloneVerifierGuidance(value.Verification.Guidance)
		payload.RequiredTests = append([]TestSelector(nil), payload.RequiredTests...)
		sort.Slice(payload.RequiredTests, func(i, j int) bool { return testSelectorLess(payload.RequiredTests[i], payload.RequiredTests[j]) })
		payload.RequiredChecks = append([]CheckSelector(nil), payload.RequiredChecks...)
		sort.Slice(payload.RequiredChecks, func(i, j int) bool { return checkSelectorLess(payload.RequiredChecks[i], payload.RequiredChecks[j]) })
		clone.Verification = &payload
	}
	return clone
}

func normalizeProcessInput(value ProcessInput) ProcessInput {
	clone := value
	clone.DesignContext = cloneDesignContext(value.DesignContext)
	clone.ScenarioSelectors = append([]ScenarioRef(nil), value.ScenarioSelectors...)
	sort.Slice(clone.ScenarioSelectors, func(i, j int) bool {
		if clone.ScenarioSelectors[i].SpecID == clone.ScenarioSelectors[j].SpecID {
			return clone.ScenarioSelectors[i].Scenario < clone.ScenarioSelectors[j].Scenario
		}
		return clone.ScenarioSelectors[i].SpecID < clone.ScenarioSelectors[j].SpecID
	})
	clone.RequiredTests = append([]TestSelector(nil), value.RequiredTests...)
	sort.Slice(clone.RequiredTests, func(i, j int) bool { return testSelectorLess(clone.RequiredTests[i], clone.RequiredTests[j]) })
	clone.RequiredChecks = append([]CheckSelector(nil), value.RequiredChecks...)
	sort.Slice(clone.RequiredChecks, func(i, j int) bool { return checkSelectorLess(clone.RequiredChecks[i], clone.RequiredChecks[j]) })
	clone.Generators = append([]GeneratorPolicy(nil), value.Generators...)
	for i := range clone.Generators {
		clone.Generators[i].RequiredOutputs = sortedCopy(clone.Generators[i].RequiredOutputs)
	}
	sort.Slice(clone.Generators, func(i, j int) bool { return clone.Generators[i].Name < clone.Generators[j].Name })
	return clone
}

func cloneDesignContext(value *DesignContext) *DesignContext {
	if value == nil {
		return nil
	}
	clone := *value
	clone.ApplicableDecisions = append([]string(nil), value.ApplicableDecisions...)
	clone.MustPreserve = append([]string(nil), value.MustPreserve...)
	clone.MustNot = append([]string(nil), value.MustNot...)
	clone.MinimumVerification = append([]string(nil), value.MinimumVerification...)
	return &clone
}

func cloneVerifierGuidance(value *VerifierGuidance) *VerifierGuidance {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Context = normalizeRawJSON(value.Context)
	clone.RulesVerify = normalizeRawJSON(value.RulesVerify)
	clone.Instructions = append([]VerifierInstruction(nil), value.Instructions...)
	sort.Slice(clone.Instructions, func(i, j int) bool {
		return clone.Instructions[i].ArtifactID < clone.Instructions[j].ArtifactID
	})
	return &clone
}

func normalizeRawJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return append(json.RawMessage(nil), value...)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return append(json.RawMessage(nil), value...)
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return append(json.RawMessage(nil), value...)
	}
	return normalized
}

func normalizeReceipt(value Receipt) Receipt {
	clone := value
	clone.Tests = append([]TestResult(nil), value.Tests...)
	sort.Slice(clone.Tests, func(i, j int) bool {
		if clone.Tests[i].ID == clone.Tests[j].ID {
			return clone.Tests[i].Command < clone.Tests[j].Command
		}
		return clone.Tests[i].ID < clone.Tests[j].ID
	})
	if value.Implementation != nil {
		payload := *value.Implementation
		payload.ChangedPaths = sortedCopy(payload.ChangedPaths)
		payload.Decisions = sortedCopy(payload.Decisions)
		payload.Risks = sortedCopy(payload.Risks)
		clone.Implementation = &payload
	}
	if value.Review != nil {
		payload := *value.Review
		payload.Findings = append([]Finding(nil), payload.Findings...)
		sort.Slice(payload.Findings, func(i, j int) bool { return payload.Findings[i].ID < payload.Findings[j].ID })
		clone.Review = &payload
	}
	if value.Verification != nil {
		payload := *value.Verification
		payload.CheckSelectors = append([]CheckSelector(nil), payload.CheckSelectors...)
		sort.Slice(payload.CheckSelectors, func(i, j int) bool { return checkSelectorLess(payload.CheckSelectors[i], payload.CheckSelectors[j]) })
		clone.Verification = &payload
	}
	return clone
}

func sortedCopy(values []string) []string {
	copyOfValues := append([]string(nil), values...)
	sort.Strings(copyOfValues)
	return copyOfValues
}

func testSelectorLess(a, b TestSelector) bool {
	if a.ID == b.ID {
		return a.Command < b.Command
	}
	return a.ID < b.ID
}
func checkSelectorLess(a, b CheckSelector) bool {
	if a.Provider == b.Provider {
		return a.Name < b.Name
	}
	return a.Provider < b.Provider
}
