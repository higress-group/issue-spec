package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

const (
	LegacyPlanVersion = 1
	// PlanVersion remains the source-compatible v1 constant for callers that
	// still compile upsert/transition plans. New relationship plans use
	// PlanVersion2 explicitly.
	PlanVersion  = LegacyPlanVersion
	PlanVersion2 = 2
)

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
	// RelationshipUpdate is a v2 owner-only relationship mutation. Every
	// target is read-only; the operation's Target is the sole write endpoint.
	RelationshipUpdate *RelationshipUpdate `json:"relationship_update,omitempty"`
	// CarrierAuthorizedBacklink requires Target to be an immutable accepted
	// receipt carrier that already links the exact Peer. Reconcile may mutate
	// only Peer to add the missing reverse link.
	CarrierAuthorizedBacklink bool `json:"carrier_authorized_backlink,omitempty"`
}

const RelationshipUpdateVersion = 1

type RelationshipUpdate struct {
	Version int                  `json:"version"`
	Add     []RelationshipTarget `json:"add,omitempty"`
	Remove  []RelationshipTarget `json:"remove,omitempty"`
}

type RelationshipTarget struct {
	Target Target `json:"target"`
	URL    string `json:"url,omitempty"`
}

// RelationshipAuthority binds a production relationship target to the exact
// provider observations from which the accepted carrier authorized its ID.
// The representation digests make retries fail closed if that authority is
// changed after resolution.
type RelationshipAuthority struct {
	CarrierURL                  string  `json:"carrier_url"`
	CarrierBodyDigest           string  `json:"carrier_body_digest"`
	PeerURL                     string  `json:"peer_url"`
	AssignmentProcess           *Target `json:"assignment_process"`
	AssignmentProcessURL        string  `json:"assignment_process_url"`
	AssignmentProcessBodyDigest string  `json:"assignment_process_body_digest"`
	AssignmentID                string  `json:"assignment_id"`
	AssignmentDigest            string  `json:"assignment_digest"`
	AssignmentGeneration        uint64  `json:"assignment_generation"`
}

type Precondition struct {
	RepresentationVersion int64                           `json:"representation_version,omitempty"`
	BodyDigest            string                          `json:"body_digest,omitempty"`
	Endpoints             []EndpointPrecondition          `json:"endpoints,omitempty"`
	AcceptedReceipt       *model.AcceptedReceiptAuthority `json:"accepted_receipt,omitempty"`
	RelationshipAuthority *RelationshipAuthority          `json:"relationship_authority,omitempty"`
}

// EndpointPrecondition binds one link endpoint to both its exact planned
// representation and its exact satisfied representation. AfterDigest lets a
// retry distinguish its own completed half-link from unrelated drift.
type EndpointPrecondition struct {
	Target                Target `json:"target"`
	RepresentationVersion int64  `json:"representation_version,omitempty"`
	BodyDigest            string `json:"body_digest,omitempty"`
	AfterDigest           string `json:"after_digest"`
}

// EndpointResult identifies one provider representation actually mutated by
// a link operation. OperationResult's existing fields remain the compatible
// primary result; Endpoints carries every successful endpoint write.
type EndpointResult struct {
	Target       Target `json:"target"`
	CommentID    int64  `json:"comment_id"`
	URL          string `json:"url,omitempty"`
	BeforeDigest string `json:"before_digest"`
	AfterDigest  string `json:"after_digest"`
}

type OperationResult struct {
	ID           string                          `json:"id"`
	Kind         string                          `json:"kind"`
	Status       string                          `json:"status"`
	Atomic       bool                            `json:"atomic"`
	Guarantee    github.CommentMutationGuarantee `json:"guarantee,omitempty"`
	CommentID    int64                           `json:"comment_id,omitempty"`
	URL          string                          `json:"url,omitempty"`
	BeforeDigest string                          `json:"before_digest"`
	AfterDigest  string                          `json:"after_digest"`
	Endpoints    []EndpointResult                `json:"endpoints,omitempty"`
	Message      string                          `json:"message,omitempty"`
}

type Result struct {
	OK          bool              `json:"ok"`
	PlanDigest  string            `json:"plan_digest"`
	Checkpoint  string            `json:"checkpoint,omitempty"`
	Atomic      bool              `json:"atomic"`
	Created     int               `json:"created"`
	Updated     int               `json:"updated"`
	Unchanged   int               `json:"unchanged"`
	Conflicted  int               `json:"conflicted"`
	Pending     int               `json:"pending"`
	Remediation string            `json:"remediation,omitempty"`
	Operations  []OperationResult `json:"operations"`
	Change      any               `json:"change,omitempty"`
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
	if plan.Version != LegacyPlanVersion && plan.Version != PlanVersion2 {
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
			if plan.Version != LegacyPlanVersion {
				return nil, "", fmt.Errorf("operation %s: legacy link is only valid in plan version 1", op.ID)
			}
			if op.Desired.Peer == nil {
				return nil, "", fmt.Errorf("operation %s: desired peer is required", op.ID)
			}
			if err := validateTarget(*op.Desired.Peer); err != nil {
				return nil, "", fmt.Errorf("operation %s peer: %w", op.ID, err)
			}
		case "relationship-update":
			if plan.Version != PlanVersion2 {
				return nil, "", fmt.Errorf("operation %s: relationship-update requires plan version %d", op.ID, PlanVersion2)
			}
			if err := validateRelationshipUpdate(op); err != nil {
				return nil, "", fmt.Errorf("operation %s: %w", op.ID, err)
			}
		default:
			return nil, "", fmt.Errorf("operation %s: unsupported kind %q", op.ID, op.Kind)
		}
		if op.Desired.CarrierAuthorizedBacklink && op.Kind != "link" {
			return nil, "", fmt.Errorf("operation %s: carrier-authorized backlink requires a link", op.ID)
		}
		if op.Precondition.RelationshipAuthority != nil &&
			(op.Precondition.AcceptedReceipt == nil || (op.Kind != "relationship-update" &&
				(op.Kind != "link" || !op.Desired.CarrierAuthorizedBacklink))) {
			return nil, "", fmt.Errorf("operation %s: resolved relationship authority requires an accepted receipt backlink", op.ID)
		}
		if op.Precondition.RepresentationVersion < 0 || (op.Precondition.RepresentationVersion > 0 && op.Precondition.BodyDigest != "") {
			return nil, "", fmt.Errorf("operation %s: representation version and body digest preconditions are mutually exclusive", op.ID)
		}
		if err := validateEndpointPreconditions(op); err != nil {
			return nil, "", fmt.Errorf("operation %s: %w", op.ID, err)
		}
		if expected := op.Precondition.AcceptedReceipt; expected != nil {
			if !validReceiptRole(expected.Role) || !projectionReceiptID.MatchString(expected.ReceiptID) ||
				!projectionDigest.MatchString(expected.Digest) || expected.Generation == 0 {
				return nil, "", fmt.Errorf("operation %s: accepted receipt precondition is invalid", op.ID)
			}
			if carrierTypes[expected.Role] != strings.ToUpper(strings.TrimSpace(op.Target.Type)) {
				return nil, "", fmt.Errorf("operation %s: accepted %s receipt precondition targets %s", op.ID, expected.Role, op.Target.Type)
			}
			switch op.Kind {
			case "transition":
				if strings.ToLower(strings.TrimSpace(op.Desired.Status)) != "done" || op.Desired.Body != "" ||
					op.Desired.BodyFile != "" || op.Desired.Handoff != "" || op.Desired.AppendHandoff ||
					len(op.Desired.PRLinks) != 0 || len(op.Desired.RelatedLinks) != 0 || op.Desired.Peer != nil || op.Desired.RelationshipUpdate != nil ||
					op.Desired.CarrierAuthorizedBacklink {
					return nil, "", fmt.Errorf("operation %s: accepted receipt precondition may only assert the carrier's immutable done status", op.ID)
				}
			case "link":
				if !op.Desired.CarrierAuthorizedBacklink || op.Precondition.RepresentationVersion != 0 ||
					op.Precondition.BodyDigest != "" || op.Desired.Body != "" || op.Desired.BodyFile != "" ||
					op.Desired.Status != "" || op.Desired.Handoff != "" || op.Desired.AppendHandoff ||
					len(op.Desired.PRLinks) != 0 || len(op.Desired.RelatedLinks) != 0 {
					return nil, "", fmt.Errorf("operation %s: accepted receipt link may only backfill a relationship explicitly carried by the immutable carrier", op.ID)
				}
				peerType := strings.ToUpper(strings.TrimSpace(op.Desired.Peer.Type))
				if sameProjectionTarget(op.Target, *op.Desired.Peer) ||
					(!relationshipTargets[expected.Role]["coverage"][peerType] && !relationshipTargets[expected.Role]["current"][peerType]) {
					return nil, "", fmt.Errorf("operation %s: accepted %s receipt cannot authorize a %s backlink target", op.ID, expected.Role, peerType)
				}
				if authority := op.Precondition.RelationshipAuthority; authority != nil {
					if authority.AssignmentProcess == nil || strings.TrimSpace(authority.CarrierURL) == "" ||
						strings.TrimSpace(authority.PeerURL) == "" || strings.TrimSpace(authority.AssignmentProcessURL) == "" ||
						!projectionDigest.MatchString(authority.CarrierBodyDigest) ||
						!projectionDigest.MatchString(authority.AssignmentProcessBodyDigest) ||
						!projectionReceiptID.MatchString(authority.AssignmentID) ||
						!projectionDigest.MatchString(authority.AssignmentDigest) || authority.AssignmentGeneration == 0 {
						return nil, "", fmt.Errorf("operation %s: resolved relationship authority is invalid", op.ID)
					}
					if err := validateTarget(*authority.AssignmentProcess); err != nil {
						return nil, "", fmt.Errorf("operation %s assignment process: %w", op.ID, err)
					}
					if expected.AssignmentID != "" && (expected.AssignmentID != authority.AssignmentID ||
						expected.AssignmentDigest != authority.AssignmentDigest || expected.Generation != authority.AssignmentGeneration) {
						return nil, "", fmt.Errorf("operation %s: accepted receipt and relationship assignment authority differ", op.ID)
					}
				}
			case "relationship-update":
				if op.Desired.RelationshipUpdate == nil || op.Desired.Peer != nil || op.Desired.Body != "" ||
					op.Desired.BodyFile != "" || op.Desired.Status != "" || op.Desired.Handoff != "" ||
					op.Desired.AppendHandoff || len(op.Desired.PRLinks) != 0 || len(op.Desired.RelatedLinks) != 0 ||
					op.Desired.CarrierAuthorizedBacklink {
					return nil, "", fmt.Errorf("operation %s: accepted receipt relationship-update may only mutate the carrier owner URL set", op.ID)
				}
				for _, relationship := range append(append([]RelationshipTarget(nil), op.Desired.RelationshipUpdate.Add...), op.Desired.RelationshipUpdate.Remove...) {
					peerType := strings.ToUpper(strings.TrimSpace(relationship.Target.Type))
					if sameProjectionTarget(op.Target, relationship.Target) ||
						(!relationshipTargets[expected.Role]["coverage"][peerType] && !relationshipTargets[expected.Role]["current"][peerType]) {
						return nil, "", fmt.Errorf("operation %s: accepted %s receipt cannot authorize a %s relationship target", op.ID, expected.Role, peerType)
					}
				}
				if authority := op.Precondition.RelationshipAuthority; authority != nil {
					if err := validateRelationshipAuthority(*expected, authority, false); err != nil {
						return nil, "", fmt.Errorf("operation %s: %w", op.ID, err)
					}
				}
			default:
				return nil, "", fmt.Errorf("operation %s: accepted receipt precondition requires a carrier assertion or carrier-authorized backlink", op.ID)
			}
		} else if op.Desired.CarrierAuthorizedBacklink {
			return nil, "", fmt.Errorf("operation %s: carrier-authorized backlink requires an accepted receipt precondition", op.ID)
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

func validateRelationshipAuthority(expected model.AcceptedReceiptAuthority, authority *RelationshipAuthority, requirePeer bool) error {
	if authority == nil || authority.AssignmentProcess == nil || strings.TrimSpace(authority.CarrierURL) == "" ||
		strings.TrimSpace(authority.AssignmentProcessURL) == "" || !projectionDigest.MatchString(authority.CarrierBodyDigest) ||
		!projectionDigest.MatchString(authority.AssignmentProcessBodyDigest) || !projectionReceiptID.MatchString(authority.AssignmentID) ||
		!projectionDigest.MatchString(authority.AssignmentDigest) || authority.AssignmentGeneration == 0 {
		return fmt.Errorf("resolved relationship authority is invalid")
	}
	if requirePeer && strings.TrimSpace(authority.PeerURL) == "" {
		return fmt.Errorf("resolved relationship peer authority is invalid")
	}
	if err := validateTarget(*authority.AssignmentProcess); err != nil {
		return fmt.Errorf("assignment process: %w", err)
	}
	if expected.AssignmentID != "" && (expected.AssignmentID != authority.AssignmentID ||
		expected.AssignmentDigest != authority.AssignmentDigest || expected.Generation != authority.AssignmentGeneration) {
		return fmt.Errorf("accepted receipt and relationship assignment authority differ")
	}
	return nil
}

func validateRelationshipUpdate(op Operation) error {
	update := op.Desired.RelationshipUpdate
	if update == nil || update.Version != RelationshipUpdateVersion || len(update.Add)+len(update.Remove) == 0 {
		return fmt.Errorf("relationship_update version %d with at least one target is required", RelationshipUpdateVersion)
	}
	if len(update.Add)+len(update.Remove) > 64 {
		return fmt.Errorf("relationship update target bound exceeded")
	}
	seen := map[string]string{}
	for action, values := range map[string][]RelationshipTarget{"add": update.Add, "remove": update.Remove} {
		for _, relationship := range values {
			if err := validateTarget(relationship.Target); err != nil {
				return fmt.Errorf("%s target: %w", action, err)
			}
			key := targetKey(relationship.Target)
			if prior := seen[key]; prior != "" && prior != action {
				return fmt.Errorf("target %s appears in both add and remove", key)
			}
			seen[key] = action
			if strings.TrimSpace(relationship.URL) != "" && model.NormalizeURL(relationship.URL) != relationship.URL {
				return fmt.Errorf("%s target %s URL must be canonical", action, relationship.Target.ID)
			}
		}
	}
	return nil
}

func validReceiptRole(role assignment.Role) bool {
	return role == assignment.RoleImplementation || role == assignment.RoleReview || role == assignment.RoleVerification
}

func validateEndpointPreconditions(op Operation) error {
	if len(op.Precondition.Endpoints) == 0 {
		return nil
	}
	if op.Kind != "link" || op.Desired.Peer == nil {
		return fmt.Errorf("endpoint preconditions require a link operation")
	}
	if op.Precondition.RepresentationVersion > 0 || op.Precondition.BodyDigest != "" {
		return fmt.Errorf("endpoint and legacy primary representation preconditions are mutually exclusive")
	}
	if sameProjectionTarget(op.Target, *op.Desired.Peer) {
		return fmt.Errorf("endpoint preconditions require distinct link endpoints")
	}
	want := map[string]bool{projectionTargetKey(op.Target): !op.Desired.CarrierAuthorizedBacklink,
		projectionTargetKey(*op.Desired.Peer): true}
	seen := map[string]bool{}
	for index, endpoint := range op.Precondition.Endpoints {
		if err := validateTarget(endpoint.Target); err != nil {
			return fmt.Errorf("endpoint preconditions[%d]: %w", index, err)
		}
		key := projectionTargetKey(endpoint.Target)
		if !want[key] {
			return fmt.Errorf("endpoint preconditions[%d] does not identify a mutable link endpoint", index)
		}
		if seen[key] {
			return fmt.Errorf("duplicate endpoint precondition for %s", key)
		}
		seen[key] = true
		if endpoint.RepresentationVersion < 0 ||
			(endpoint.RepresentationVersion > 0 && endpoint.BodyDigest != "") ||
			(endpoint.RepresentationVersion == 0 && endpoint.BodyDigest == "") {
			return fmt.Errorf("endpoint preconditions[%d] requires exactly one representation version or body digest", index)
		}
		if endpoint.BodyDigest != "" && !projectionDigest.MatchString(normalizeDigest(endpoint.BodyDigest)) {
			return fmt.Errorf("endpoint preconditions[%d] body digest is invalid", index)
		}
		if !projectionDigest.MatchString(normalizeDigest(endpoint.AfterDigest)) {
			return fmt.Errorf("endpoint preconditions[%d] after digest is invalid", index)
		}
	}
	for key, required := range want {
		if required && !seen[key] {
			return fmt.Errorf("endpoint precondition for %s is required", key)
		}
	}
	return nil
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
