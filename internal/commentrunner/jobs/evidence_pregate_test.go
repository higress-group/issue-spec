package jobs

import (
	"context"
	"os"
	"testing"
)

func TestValidateEvidencePreGateResult(t *testing.T) {
	if err := validateEvidencePreGateResult(EvidencePreGateResult{Skipped: true}); err != nil {
		t.Fatalf("skipped pre-gate = %v", err)
	}
	valid := EvidencePreGateResult{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "42", SubjectRevision: "abc", EvidenceIDs: []string{"evidence-a", "evidence-b"}}
	if err := validateEvidencePreGateResult(valid); err != nil {
		t.Fatalf("valid pre-gate = %v", err)
	}
	invalid := valid
	invalid.EvidenceIDs = []string{"evidence-a", "evidence-a"}
	if err := validateEvidencePreGateResult(invalid); err == nil {
		t.Fatal("duplicate evidence identity was accepted")
	}
	invalid = valid
	invalid.SubjectRevision = ""
	if err := validateEvidencePreGateResult(invalid); err == nil {
		t.Fatal("incomplete evidence identity was accepted")
	}
}

type recordingEvidencePreGate struct {
	calls               int
	request             EvidencePreGateRequest
	credentialAvailable bool
}

func (g *recordingEvidencePreGate) BeforeDispatch(_ context.Context, request EvidencePreGateRequest) (EvidencePreGateResult, error) {
	g.calls++
	g.request = request
	if raw, err := os.ReadFile(request.CredentialFile); err == nil && len(raw) > 0 {
		g.credentialAvailable = true
		clear(raw)
	}
	return EvidencePreGateResult{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "42", SubjectRevision: "abc", EvidenceIDs: []string{"evidence-a"}}, nil
}
