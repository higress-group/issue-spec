package reconcile

import (
	"reflect"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/assignment"
)

func TestCompileReceiptProjectionDeterministicExistingOperations(t *testing.T) {
	input := ReceiptProjection{Version: 1, Repo: "o/r", Hostname: "issues.example", Proposal: 7, Issue: 9, AllowNonAtomic: true,
		AcceptedReceipts: []AcceptedReceiptProjection{
			{Role: assignment.RoleReview, Carrier: Target{Type: "review", ID: "REVIEW-002"}, ReceiptID: "review-2",
				ReceiptDigest: strings.Repeat("b", 64), Generation: 2,
				Lifecycle:       []ReceiptLifecycle{{Target: Target{Type: "review", ID: "REVIEW-002"}, Status: "done"}},
				CoverageTargets: []Target{{Type: "spec", ID: "SPEC-002"}, {Type: "process", ID: "PROCESS-002"}},
				CurrentTargets:  []Target{{Type: "process", ID: "PROCESS-009"}}},
			{Role: assignment.RoleImplementation, Carrier: Target{Type: "process", ID: "PROCESS-001"}, ReceiptID: "implementation-1",
				ReceiptDigest: strings.Repeat("a", 64), Generation: 1,
				Lifecycle:       []ReceiptLifecycle{{Target: Target{Type: "process", ID: "PROCESS-001"}, Status: "done"}},
				CoverageTargets: []Target{{Type: "spec", ID: "SPEC-001"}, {Type: "task", ID: "TASK-001"}},
				CurrentTargets:  []Target{{Type: "task", ID: "TASK-009"}}},
		}}

	first, err := CompileReceiptProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	input.AcceptedReceipts[0], input.AcceptedReceipts[1] = input.AcceptedReceipts[1], input.AcceptedReceipts[0]
	for index := range input.AcceptedReceipts {
		receipt := &input.AcceptedReceipts[index]
		reverseTargets(receipt.CoverageTargets)
		reverseLifecycle(receipt.Lifecycle)
	}
	second, err := CompileReceiptProjection(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.PlanDigest == "" {
		t.Fatalf("compiled plans differ\nfirst=%+v\nsecond=%+v", first, second)
	}
	for _, operation := range first.Operations {
		if operation.Kind != "transition" && operation.Kind != "link" {
			t.Fatalf("projection emitted unsupported operation %+v", operation)
		}
		if operation.Kind == "upsert" || operation.Desired.Body != "" || operation.Desired.BodyFile != "" {
			t.Fatalf("projection emitted arbitrary body mutation %+v", operation)
		}
		if operation.Kind == "link" && (!operation.Desired.CarrierAuthorizedBacklink ||
			operation.Precondition.AcceptedReceipt == nil || operation.Target.Type != carrierTypes[operation.Precondition.AcceptedReceipt.Role]) {
			t.Fatalf("projection emitted a relationship without sealed carrier authority %+v", operation)
		}
	}
	if !first.AllowNonAtomic || first.Operations[0].Target.Type != "PROCESS" || len(first.Operations[0].Desired.RelatedLinks) != 0 ||
		first.Operations[0].Precondition.AcceptedReceipt == nil ||
		first.Operations[0].Precondition.AcceptedReceipt.ReceiptID != "implementation-1" {
		t.Fatalf("implementation carrier lifecycle=%+v", first.Operations[0])
	}
	if _, digest, err := Validate(first); err != nil || digest != first.PlanDigest {
		t.Fatalf("compiled plan validation digest=%s err=%v plan=%+v", digest, err, first)
	}
	input.AllowNonAtomic = false
	strict, err := CompileReceiptProjection(input)
	if err != nil || strict.AllowNonAtomic || strict.PlanDigest == first.PlanDigest {
		t.Fatalf("non-atomic policy was not bound into compiled plan strict=%+v err=%v", strict, err)
	}
}

func TestCompileReceiptProjectionRejectsInvalidRelationshipsBeforePlan(t *testing.T) {
	valid := AcceptedReceiptProjection{Role: assignment.RoleReview, Carrier: Target{Type: "REVIEW", ID: "REVIEW-001"},
		ReceiptID: "review-1", ReceiptDigest: strings.Repeat("c", 64), Generation: 1,
		Lifecycle:       []ReceiptLifecycle{{Target: Target{Type: "REVIEW", ID: "REVIEW-001"}, Status: "done"}},
		CoverageTargets: []Target{{Type: "SPEC", ID: "SPEC-001"}}, CurrentTargets: []Target{{Type: "PROCESS", ID: "PROCESS-001"}}}
	tests := map[string]func(*AcceptedReceiptProjection){
		"carrier": func(value *AcceptedReceiptProjection) { value.Carrier.Type = "PROCESS" },
		"question target": func(value *AcceptedReceiptProjection) {
			value.CoverageTargets = []Target{{Type: "QUESTION", ID: "QUESTION-001"}}
		},
		"relationship matrix": func(value *AcceptedReceiptProjection) {
			value.CurrentTargets = []Target{{Type: "TASK", ID: "TASK-001"}}
		},
		"provider URL as typed peer": func(value *AcceptedReceiptProjection) {
			value.CoverageTargets = []Target{{Type: "https://code.example/review/1", ID: "SPEC-001"}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Lifecycle = append([]ReceiptLifecycle(nil), valid.Lifecycle...)
			candidate.CoverageTargets = append([]Target(nil), valid.CoverageTargets...)
			candidate.CurrentTargets = append([]Target(nil), valid.CurrentTargets...)
			mutate(&candidate)
			if plan, err := CompileReceiptProjection(ReceiptProjection{Version: 1, Repo: "o/r", Hostname: "issues.example",
				Proposal: 7, Issue: 9,
				AcceptedReceipts: []AcceptedReceiptProjection{candidate}}); err == nil || len(plan.Operations) != 0 {
				t.Fatalf("invalid projection compiled plan=%+v err=%v", plan, err)
			}
		})
	}
}

func TestCompileReceiptProjectionRequiresExplicitSafeLifecycleAndRelationships(t *testing.T) {
	valid := AcceptedReceiptProjection{Role: assignment.RoleVerification,
		Carrier: Target{Type: "VERIFY", ID: "VERIFY-001"}, ReceiptID: "verification-1",
		ReceiptDigest: strings.Repeat("d", 64), Generation: 1,
		Lifecycle:       []ReceiptLifecycle{{Target: Target{Type: "VERIFY", ID: "VERIFY-001"}, Status: "done"}},
		CoverageTargets: []Target{{Type: "SPEC", ID: "SPEC-001"}},
		CurrentTargets:  []Target{{Type: "PROCESS", ID: "PROCESS-001"}}}
	tests := map[string]func(*AcceptedReceiptProjection){
		"missing lifecycle": func(value *AcceptedReceiptProjection) { value.Lifecycle = nil },
		"missing carrier lifecycle": func(value *AcceptedReceiptProjection) {
			value.Lifecycle = []ReceiptLifecycle{{Target: Target{Type: "PROCESS", ID: "PROCESS-001"}, Status: "done"}}
		},
		"additional lifecycle target": func(value *AcceptedReceiptProjection) {
			value.Lifecycle = append(value.Lifecycle, ReceiptLifecycle{Target: Target{Type: "TASK", ID: "TASK-001"}, Status: "done"})
		},
		"carrier superseded":  func(value *AcceptedReceiptProjection) { value.Lifecycle[0].Status = "superseded" },
		"carrier in progress": func(value *AcceptedReceiptProjection) { value.Lifecycle[0].Status = "in-progress" },
		"missing coverage":    func(value *AcceptedReceiptProjection) { value.CoverageTargets = nil },
		"missing current":     func(value *AcceptedReceiptProjection) { value.CurrentTargets = nil },
		"mismatched typed id": func(value *AcceptedReceiptProjection) {
			value.CurrentTargets = []Target{{Type: "PROCESS", ID: "SPEC-001"}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Lifecycle = append([]ReceiptLifecycle(nil), valid.Lifecycle...)
			candidate.CoverageTargets = append([]Target(nil), valid.CoverageTargets...)
			candidate.CurrentTargets = append([]Target(nil), valid.CurrentTargets...)
			mutate(&candidate)
			if _, err := CompileReceiptProjection(ReceiptProjection{Version: 1, Repo: "o/r", Hostname: "issues.example",
				Proposal: 7, Issue: 9, AcceptedReceipts: []AcceptedReceiptProjection{candidate}}); err == nil {
				t.Fatal("unsafe projection compiled")
			}
		})
	}
}

func reverseTargets(values []Target) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseLifecycle(values []ReceiptLifecycle) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
