package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/model"
)

const PlanVersion = 1

type Plan struct {
	Version        int         `json:"version"`
	PlanDigest     string      `json:"plan_digest,omitempty"`
	Repo           string      `json:"repo"`
	Hostname       string      `json:"hostname,omitempty"`
	Proposal       int         `json:"proposal,omitempty"`
	AllowNonAtomic bool        `json:"allow_nonatomic,omitempty"`
	Operations     []Operation `json:"operations"`
}

type Operation struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	DependsOn    []string     `json:"depends_on,omitempty"`
	Target       Target       `json:"target"`
	Desired      Desired      `json:"desired"`
	Precondition Precondition `json:"precondition,omitempty"`
}

type Target struct {
	Role  string `json:"role,omitempty"`
	Issue int    `json:"issue,omitempty"`
	Type  string `json:"type"`
	ID    string `json:"id"`
}

type Desired struct {
	Body          string   `json:"body,omitempty"`
	BodyFile      string   `json:"body_file,omitempty"`
	Status        string   `json:"status,omitempty"`
	Handoff       string   `json:"handoff,omitempty"`
	AppendHandoff bool     `json:"append_handoff,omitempty"`
	PRLinks       []string `json:"pr_links,omitempty"`
	RelatedLinks  []string `json:"related_links,omitempty"`
	Peer          *Target  `json:"peer,omitempty"`
}

type Precondition struct {
	RepresentationVersion int64                           `json:"representation_version,omitempty"`
	BodyDigest            string                          `json:"body_digest,omitempty"`
	AcceptedReceipt       *model.AcceptedReceiptAuthority `json:"accepted_receipt,omitempty"`
}

type OperationResult struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Atomic    bool   `json:"atomic"`
	CommentID int64  `json:"comment_id,omitempty"`
	URL       string `json:"url,omitempty"`
	Message   string `json:"message,omitempty"`
}

type Result struct {
	OK         bool              `json:"ok"`
	PlanDigest string            `json:"plan_digest"`
	Checkpoint string            `json:"checkpoint,omitempty"`
	Atomic     bool              `json:"atomic"`
	Created    int               `json:"created"`
	Updated    int               `json:"updated"`
	Unchanged  int               `json:"unchanged"`
	Conflicted int               `json:"conflicted"`
	Pending    int               `json:"pending"`
	Operations []OperationResult `json:"operations"`
	Change     any               `json:"change,omitempty"`
}

func Digest(plan Plan) (string, error) {
	plan.PlanDigest = ""
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func Validate(plan Plan) ([]Operation, string, error) {
	if plan.Version != PlanVersion {
		return nil, "", fmt.Errorf("unsupported plan version %d", plan.Version)
	}
	if strings.TrimSpace(plan.Repo) == "" || len(plan.Operations) == 0 {
		return nil, "", fmt.Errorf("repo and operations are required")
	}
	digest, err := Digest(plan)
	if err != nil {
		return nil, "", err
	}
	if plan.PlanDigest != "" && normalizeDigest(plan.PlanDigest) != digest {
		return nil, "", fmt.Errorf("plan digest mismatch: declared=%s computed=%s", normalizeDigest(plan.PlanDigest), digest)
	}
	byID := map[string]Operation{}
	upserts := map[string]string{}
	for _, op := range plan.Operations {
		op.ID = strings.TrimSpace(op.ID)
		op.Kind = strings.ToLower(strings.TrimSpace(op.Kind))
		if op.ID == "" {
			return nil, "", fmt.Errorf("operation id is required")
		}
		if _, exists := byID[op.ID]; exists {
			return nil, "", fmt.Errorf("duplicate operation id %s", op.ID)
		}
		if err := validateTarget(op.Target); err != nil {
			return nil, "", fmt.Errorf("operation %s: %w", op.ID, err)
		}
		switch op.Kind {
		case "upsert":
			if strings.TrimSpace(op.Desired.Body) == "" || op.Desired.BodyFile != "" {
				return nil, "", fmt.Errorf("operation %s: resolved desired body is required", op.ID)
			}
			key := targetKey(op.Target)
			if prior := upserts[key]; prior != "" {
				return nil, "", fmt.Errorf("duplicate logical artifact %s in %s and %s", key, prior, op.ID)
			}
			upserts[key] = op.ID
		case "transition":
			if strings.TrimSpace(op.Desired.Status) == "" {
				return nil, "", fmt.Errorf("operation %s: desired status is required", op.ID)
			}
		case "link":
			if op.Desired.Peer == nil {
				return nil, "", fmt.Errorf("operation %s: desired peer is required", op.ID)
			}
			if err := validateTarget(*op.Desired.Peer); err != nil {
				return nil, "", fmt.Errorf("operation %s peer: %w", op.ID, err)
			}
		default:
			return nil, "", fmt.Errorf("operation %s: unsupported kind %q", op.ID, op.Kind)
		}
		if op.Precondition.RepresentationVersion < 0 || (op.Precondition.RepresentationVersion > 0 && op.Precondition.BodyDigest != "") {
			return nil, "", fmt.Errorf("operation %s: representation version and body digest preconditions are mutually exclusive", op.ID)
		}
		if expected := op.Precondition.AcceptedReceipt; expected != nil {
			if op.Kind != "transition" {
				return nil, "", fmt.Errorf("operation %s: accepted receipt precondition requires a transition", op.ID)
			}
			if !validReceiptRole(expected.Role) || !projectionReceiptID.MatchString(expected.ReceiptID) ||
				!projectionDigest.MatchString(expected.Digest) || expected.Generation == 0 {
				return nil, "", fmt.Errorf("operation %s: accepted receipt precondition is invalid", op.ID)
			}
			if carrierTypes[expected.Role] != strings.ToUpper(strings.TrimSpace(op.Target.Type)) {
				return nil, "", fmt.Errorf("operation %s: accepted %s receipt precondition targets %s", op.ID, expected.Role, op.Target.Type)
			}
			if strings.ToLower(strings.TrimSpace(op.Desired.Status)) != "done" || op.Desired.Body != "" ||
				op.Desired.BodyFile != "" || op.Desired.Handoff != "" || op.Desired.AppendHandoff ||
				len(op.Desired.PRLinks) != 0 || len(op.Desired.RelatedLinks) != 0 || op.Desired.Peer != nil {
				return nil, "", fmt.Errorf("operation %s: accepted receipt precondition may only assert the carrier's immutable done status", op.ID)
			}
		}
		byID[op.ID] = op
	}
	indegree := map[string]int{}
	children := map[string][]string{}
	for id, op := range byID {
		for _, dep := range op.DependsOn {
			if _, ok := byID[dep]; !ok {
				return nil, "", fmt.Errorf("operation %s depends on unknown operation %s", id, dep)
			}
			if dep == id {
				return nil, "", fmt.Errorf("operation %s depends on itself", id)
			}
			indegree[id]++
			children[dep] = append(children[dep], id)
		}
	}
	var ready []string
	for id := range byID {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	var ordered []Operation
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(plan.Operations) {
		return nil, "", fmt.Errorf("operation dependency cycle detected")
	}
	return ordered, digest, nil
}

func validReceiptRole(role assignment.Role) bool {
	return role == assignment.RoleImplementation || role == assignment.RoleReview || role == assignment.RoleVerification
}

func validateTarget(target Target) error {
	if target.Issue <= 0 && strings.TrimSpace(target.Role) == "" {
		return fmt.Errorf("target issue or role is required")
	}
	if target.Issue > 0 && target.Role != "" {
		return fmt.Errorf("target issue and role are mutually exclusive")
	}
	if strings.TrimSpace(target.Type) == "" || strings.TrimSpace(target.ID) == "" {
		return fmt.Errorf("target type and id are required")
	}
	return nil
}
func targetKey(t Target) string {
	return fmt.Sprintf("%d:%s:%s", t.Issue, strings.ToUpper(t.Type), t.ID)
}
func normalizeDigest(v string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "sha256:"))
}
