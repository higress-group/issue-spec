package reconcile

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/model"
)

const ReceiptProjectionVersion = 1

// ReceiptProjection is a declarative request to project references that were
// already accepted by the role-specific receipt commands. It carries identity
// only: the immutable receipt payload, provenance, assurance, and provider
// evidence remain in their accepted carriers.
type ReceiptProjection struct {
	Version          int                         `json:"version"`
	Repo             string                      `json:"repo"`
	Hostname         string                      `json:"hostname"`
	Proposal         int                         `json:"proposal"`
	Issue            int                         `json:"issue"`
	AllowNonAtomic   bool                        `json:"allow_nonatomic,omitempty"`
	AcceptedReceipts []AcceptedReceiptProjection `json:"accepted_receipts"`
}

type AcceptedReceiptProjection struct {
	Role            assignment.Role    `json:"role"`
	Carrier         Target             `json:"carrier"`
	ReceiptID       string             `json:"receipt_id"`
	ReceiptDigest   string             `json:"receipt_digest"`
	Generation      uint64             `json:"generation"`
	Lifecycle       []ReceiptLifecycle `json:"lifecycle"`
	CoverageTargets []Target           `json:"coverage_targets"`
	CurrentTargets  []Target           `json:"current_targets"`
}

type ReceiptLifecycle struct {
	Target Target `json:"target"`
	Status string `json:"status"`
}

var projectionReceiptID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var projectionDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var projectionRepository = regexp.MustCompile(`^[^/[:space:]]+/[^/[:space:]]+$`)

var carrierTypes = map[assignment.Role]string{
	assignment.RoleImplementation: "PROCESS",
	assignment.RoleReview:         "REVIEW",
	assignment.RoleVerification:   "VERIFY",
}

var relationshipTargets = map[assignment.Role]map[string]map[string]bool{
	assignment.RoleImplementation: {
		"coverage": {"SPEC": true, "TASK": true},
		"current":  {"TASK": true},
	},
	assignment.RoleReview: {
		"coverage": {"PROCESS": true, "SPEC": true},
		"current":  {"PROCESS": true},
	},
	assignment.RoleVerification: {
		"coverage": {"PROCESS": true, "SPEC": true},
		"current":  {"PROCESS": true},
	},
}

// CompileReceiptProjection validates an accepted-receipt projection and
// lowers it to the existing reconcile transition/link plan. The returned plan
// is fully digested and contains no upsert or provider mutation operation.
func CompileReceiptProjection(input ReceiptProjection) (Plan, error) {
	if input.Version != ReceiptProjectionVersion {
		return Plan{}, fmt.Errorf("unsupported receipt projection version %d", input.Version)
	}
	input.Repo = strings.TrimSpace(input.Repo)
	input.Hostname = strings.TrimSpace(input.Hostname)
	if !projectionRepository.MatchString(input.Repo) || !validProjectionHostname(input.Hostname) ||
		input.Proposal <= 0 || input.Issue <= 0 || len(input.AcceptedReceipts) == 0 {
		return Plan{}, fmt.Errorf("repo, hostname, proposal, issue, and accepted_receipts are required and must be valid")
	}

	receipts := make([]AcceptedReceiptProjection, len(input.AcceptedReceipts))
	for index, receipt := range input.AcceptedReceipts {
		receipts[index] = cloneAcceptedReceiptProjection(receipt)
	}
	for index := range receipts {
		if err := normalizeReceiptProjection(&receipts[index], input.Issue); err != nil {
			return Plan{}, fmt.Errorf("accepted receipt %d: %w", index, err)
		}
	}
	sort.Slice(receipts, func(i, j int) bool { return receiptProjectionKey(receipts[i]) < receiptProjectionKey(receipts[j]) })

	seenID, seenDigest := map[string]bool{}, map[string]bool{}
	plan := Plan{Version: PlanVersion, Repo: input.Repo, Hostname: input.Hostname, Proposal: input.Proposal,
		AllowNonAtomic: input.AllowNonAtomic}
	for _, receipt := range receipts {
		idKey := strings.ToLower(receipt.ReceiptID)
		digestKey := receipt.ReceiptDigest
		if seenID[idKey] {
			return Plan{}, fmt.Errorf("duplicate %s receipt id %s", receipt.Role, receipt.ReceiptID)
		}
		if seenDigest[digestKey] {
			return Plan{}, fmt.Errorf("duplicate %s receipt digest %s", receipt.Role, receipt.ReceiptDigest)
		}
		seenID[idKey], seenDigest[digestKey] = true, true
		operations, err := compileAcceptedReceipt(receipt)
		if err != nil {
			return Plan{}, err
		}
		plan.Operations = append(plan.Operations, operations...)
	}
	if _, digest, err := Validate(plan); err != nil {
		return Plan{}, fmt.Errorf("validate compiled receipt projection: %w", err)
	} else {
		plan.PlanDigest = digest
	}
	return plan, nil
}

func normalizeReceiptProjection(receipt *AcceptedReceiptProjection, defaultIssue int) error {
	receipt.Role = assignment.Role(strings.ToLower(strings.TrimSpace(string(receipt.Role))))
	wantCarrier, ok := carrierTypes[receipt.Role]
	if !ok {
		return fmt.Errorf("unsupported receipt role %q", receipt.Role)
	}
	var err error
	receipt.Carrier, err = normalizeProjectionTarget(receipt.Carrier, defaultIssue)
	if err != nil {
		return fmt.Errorf("carrier: %w", err)
	}
	if receipt.Carrier.Type != wantCarrier {
		return fmt.Errorf("%s receipt carrier must be %s, got %s", receipt.Role, wantCarrier, receipt.Carrier.Type)
	}
	receipt.ReceiptID = strings.TrimSpace(receipt.ReceiptID)
	receipt.ReceiptDigest = strings.TrimSpace(receipt.ReceiptDigest)
	if !projectionReceiptID.MatchString(receipt.ReceiptID) {
		return fmt.Errorf("invalid receipt_id %q", receipt.ReceiptID)
	}
	if !projectionDigest.MatchString(receipt.ReceiptDigest) {
		return fmt.Errorf("receipt_digest must be 64 hexadecimal characters")
	}
	if receipt.Generation == 0 {
		return fmt.Errorf("generation must be positive")
	}
	if len(receipt.Lifecycle) != 1 {
		return fmt.Errorf("lifecycle must contain exactly one immutable carrier assertion")
	}

	item := &receipt.Lifecycle[0]
	item.Target, err = normalizeProjectionTarget(item.Target, defaultIssue)
	if err != nil {
		return fmt.Errorf("lifecycle carrier target: %w", err)
	}
	item.Status = strings.ToLower(strings.TrimSpace(item.Status))
	if !sameProjectionTarget(item.Target, receipt.Carrier) {
		return fmt.Errorf("lifecycle may only assert the receipt carrier")
	}
	if item.Status != "done" {
		return fmt.Errorf("accepted receipt carrier lifecycle must be the immutable done assertion, got %q", item.Status)
	}

	if receipt.CoverageTargets, err = normalizeRelationshipTargets(receipt.Role, "coverage", receipt.Carrier, receipt.CoverageTargets, defaultIssue); err != nil {
		return err
	}
	if receipt.CurrentTargets, err = normalizeRelationshipTargets(receipt.Role, "current", receipt.Carrier, receipt.CurrentTargets, defaultIssue); err != nil {
		return err
	}
	if len(receipt.CoverageTargets) == 0 || len(receipt.CurrentTargets) == 0 {
		return fmt.Errorf("coverage_targets and current_targets must be explicit and non-empty")
	}
	return nil
}

func normalizeProjectionTarget(target Target, _ int) (Target, error) {
	target.Role = strings.ToLower(strings.TrimSpace(target.Role))
	target.Type = strings.ToUpper(strings.TrimSpace(target.Type))
	target.ID = strings.TrimSpace(target.ID)
	if target.Issue == 0 && target.Role == "" {
		switch target.Type {
		case "SPEC":
			target.Role = "proposal"
		case "TASK":
			target.Role = "design"
		default:
			target.Role = "implement"
		}
	}
	if target.Role != "" && target.Role != "proposal" && target.Role != "design" && target.Role != "implement" {
		return Target{}, fmt.Errorf("target role %q is not projection-safe", target.Role)
	}
	if !model.AllowedTypes[target.Type] || target.Type == "QUESTION" {
		return Target{}, fmt.Errorf("target type %q is not projection-safe", target.Type)
	}
	if err := model.ValidateTypedIdentity(target.Type, target.ID); err != nil {
		return Target{}, err
	}
	if err := validateTarget(target); err != nil {
		return Target{}, err
	}
	return target, nil
}

func normalizeRelationshipTargets(role assignment.Role, relationship string, carrier Target, targets []Target, defaultIssue int) ([]Target, error) {
	allowed := relationshipTargets[role][relationship]
	result, seen := append([]Target(nil), targets...), map[string]bool{}
	for index := range result {
		target, err := normalizeProjectionTarget(result[index], defaultIssue)
		if err != nil {
			return nil, fmt.Errorf("%s target %d: %w", relationship, index, err)
		}
		if !allowed[target.Type] {
			return nil, fmt.Errorf("%s receipt cannot use %s as a %s target", role, target.Type, relationship)
		}
		if sameProjectionTarget(carrier, target) {
			return nil, fmt.Errorf("%s target cannot be the receipt carrier itself", relationship)
		}
		key := projectionTargetKey(target)
		if seen[key] {
			return nil, fmt.Errorf("duplicate %s target %s", relationship, key)
		}
		seen[key], result[index] = true, target
	}
	sort.Slice(result, func(i, j int) bool { return projectionTargetKey(result[i]) < projectionTargetKey(result[j]) })
	return result, nil
}

func compileAcceptedReceipt(receipt AcceptedReceiptProjection) ([]Operation, error) {
	prefix := fmt.Sprintf("receipt-%s-%s-g%d-%s", receipt.Role, receipt.ReceiptID, receipt.Generation, receipt.ReceiptDigest[:12])
	lifecycleIDs := make([]string, 0, len(receipt.Lifecycle))
	operations := make([]Operation, 0, len(receipt.Lifecycle)+len(receipt.CoverageTargets)+len(receipt.CurrentTargets))
	carrierLifecycleID := ""
	for index, lifecycle := range receipt.Lifecycle {
		id := fmt.Sprintf("%s-lifecycle-%03d", prefix, index+1)
		desired := Desired{Status: lifecycle.Status}
		precondition := Precondition{}
		if sameProjectionTarget(lifecycle.Target, receipt.Carrier) {
			carrierLifecycleID = id
			precondition.AcceptedReceipt = &model.AcceptedReceiptAuthority{Role: receipt.Role,
				ReceiptID: receipt.ReceiptID, Digest: receipt.ReceiptDigest, Generation: receipt.Generation}
		}
		operations = append(operations, Operation{ID: id, Kind: "transition", Target: lifecycle.Target,
			Desired: desired, Precondition: precondition})
		lifecycleIDs = append(lifecycleIDs, id)
	}
	for index := range operations {
		if operations[index].ID != carrierLifecycleID {
			operations[index].DependsOn = []string{carrierLifecycleID}
		}
	}
	linked := map[string]bool{}
	for _, group := range []struct {
		name    string
		targets []Target
	}{{"coverage", receipt.CoverageTargets}, {"current", receipt.CurrentTargets}} {
		for index, target := range group.targets {
			key := projectionTargetKey(target)
			if linked[key] {
				continue
			}
			linked[key] = true
			authority := &model.AcceptedReceiptAuthority{Role: receipt.Role, ReceiptID: receipt.ReceiptID,
				Digest: receipt.ReceiptDigest, Generation: receipt.Generation}
			operations = append(operations, Operation{
				ID:        fmt.Sprintf("%s-%s-%03d", prefix, group.name, index+1),
				Kind:      "link",
				DependsOn: append([]string(nil), lifecycleIDs...),
				Target:    receipt.Carrier,
				Desired: Desired{Peer: cloneTarget(target),
					CarrierAuthorizedBacklink: true},
				Precondition: Precondition{AcceptedReceipt: authority},
			})
		}
	}
	return operations, nil
}

func validProjectionHostname(hostname string) bool {
	parsed, err := url.Parse("https://" + hostname)
	return err == nil && parsed.Scheme == "https" && parsed.Host == hostname && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func cloneTarget(target Target) *Target { return &target }

func cloneAcceptedReceiptProjection(receipt AcceptedReceiptProjection) AcceptedReceiptProjection {
	receipt.Lifecycle = append([]ReceiptLifecycle(nil), receipt.Lifecycle...)
	receipt.CoverageTargets = append([]Target(nil), receipt.CoverageTargets...)
	receipt.CurrentTargets = append([]Target(nil), receipt.CurrentTargets...)
	return receipt
}

func receiptProjectionKey(receipt AcceptedReceiptProjection) string {
	return strings.Join([]string{string(receipt.Role), receipt.ReceiptID, receipt.ReceiptDigest,
		fmt.Sprint(receipt.Generation), projectionTargetKey(receipt.Carrier)}, ":")
}

func projectionTargetKey(target Target) string {
	return fmt.Sprintf("%s:%d:%s:%s", strings.ToLower(target.Role), target.Issue, strings.ToUpper(target.Type), target.ID)
}

func sameProjectionTarget(left, right Target) bool {
	return projectionTargetKey(left) == projectionTargetKey(right)
}
