package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/preview"
)

// RepresentationDigest returns the lowercase SHA-256 digest of the stripped
// raw body: CanonicalView removes a recognized machine-translation suffix
// (divider-anchored, single-divider rule) and nothing else. There is no
// preview masking and no other whitespace, newline, or semantic normalization,
// so bodies without a recognized suffix keep their exact-bytes digest and
// html-preview source edits remain digest-visible.
func RepresentationDigest(body string) string {
	sum := sha256.Sum256([]byte(CanonicalView(body)))
	return hex.EncodeToString(sum[:])
}

var idRe = regexp.MustCompile(`^[A-Z]+-[0-9]{3,}$`)
var acceptedReceiptIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var acceptedReceiptDigestRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

var AllowedTypes = map[string]bool{
	"SPEC":     true,
	"TASK":     true,
	"PROCESS":  true,
	"QUESTION": true,
	"ANSWER":   true,
	"REVIEW":   true,
	"VERIFY":   true,
}

var AllowedStatuses = map[string]bool{
	"draft":       true,
	"blocked":     true,
	"confirmed":   true,
	"in-progress": true,
	"ready":       true,
	"done":        true,
	"superseded":  true,
}

// ValidateTypedIdentity validates one issue-native typed artifact identity
// without parsing or generating a body.
func ValidateTypedIdentity(commentType, id string) error {
	commentType = strings.ToUpper(strings.TrimSpace(commentType))
	id = strings.TrimSpace(id)
	if !AllowedTypes[commentType] {
		return fmt.Errorf("unsupported type %s", commentType)
	}
	if !idRe.MatchString(id) || !strings.HasPrefix(id, commentType+"-") {
		return fmt.Errorf("invalid %s id %s", commentType, id)
	}
	return nil
}

// ValidateIssueScopedTypedIdentity validates the repository-unique identity
// assigned to a newly created typed comment. Existing legacy IDs remain
// readable through ValidateTypedIdentity and must not be renumbered.
func ValidateIssueScopedTypedIdentity(commentType, id string, issue int64) error {
	commentType = strings.ToUpper(strings.TrimSpace(commentType))
	id = strings.TrimSpace(id)
	if err := ValidateTypedIdentity(commentType, id); err != nil {
		return err
	}
	if issue <= 0 {
		return fmt.Errorf("invalid issue number %d for %s id %s", issue, commentType, id)
	}
	prefix := commentType + "-" + strconv.FormatInt(issue, 10)
	sequence := strings.TrimPrefix(id, prefix)
	if len(sequence) != 3 || sequence == "000" {
		return issueScopedTypedIdentityError(commentType, id, issue)
	}
	for _, digit := range sequence {
		if digit < '0' || digit > '9' {
			return issueScopedTypedIdentityError(commentType, id, issue)
		}
	}
	return nil
}

func issueScopedTypedIdentityError(commentType, id string, issue int64) error {
	return fmt.Errorf("invalid id %s for issue %d: expected %s-%d<NNN> (e.g. %s-%d001)",
		id, issue, commentType, issue, commentType, issue)
}

type TypedComment struct {
	Marker             Marker                   `json:"marker"`
	Agent              string                   `json:"agent"`
	AgentSessionID     string                   `json:"agent_session_id,omitempty"`
	AgentSessionSource string                   `json:"agent_session_source,omitempty"`
	SubjectRevision    string                   `json:"subject_revision,omitempty"`
	Assignment         *assignment.ProcessInput `json:"assignment,omitempty"`
	Type               string                   `json:"type"`
	ID                 string                   `json:"id"`
	Status             string                   `json:"status"`
	Scope              string                   `json:"scope"`
	Links              map[string][]string      `json:"links"`
	Body               string                   `json:"-"`
	Errors             []string                 `json:"errors,omitempty"`
	HasHead            bool                     `json:"has_header"`
}

type BodyOptions struct {
	Agent              string
	AgentSessionID     string
	AgentSessionSource string
	SubjectRevision    string
	Status             string
	Scope              string
	Links              map[string][]string
}

// AcceptedReceiptAuthority is the immutable identity shared by compact
// accepted-receipt carriers. Review and verification carriers also expose the
// durable assignment identity used to locate their authoritative PROCESS.
// Receipt content, provenance, assurance, and subject revisions deliberately
// remain in the role-owned carrier body.
type AcceptedReceiptAuthority struct {
	Role             assignment.Role `json:"role"`
	ReceiptID        string          `json:"receipt_id"`
	Digest           string          `json:"receipt_digest"`
	Generation       uint64          `json:"generation"`
	AssignmentID     string          `json:"assignment_id,omitempty"`
	AssignmentDigest string          `json:"assignment_digest,omitempty"`
}

var acceptedReceiptMarkers = map[assignment.Role]struct {
	starts []string
	end    string
	token  string
}{
	assignment.RoleImplementation: {
		starts: []string{"<!-- issue-spec:accepted-implementation-receipt version=1 -->"},
		end:    "<!-- /issue-spec:accepted-implementation-receipt -->",
		token:  "issue-spec:accepted-implementation-receipt",
	},
	assignment.RoleReview: {
		starts: []string{
			"<!-- issue-spec:accepted-review-receipt version=1 -->",
			"<!-- issue-spec:accepted-review-receipt version=2 -->",
		},
		end:   "<!-- /issue-spec:accepted-review-receipt -->",
		token: "issue-spec:accepted-review-receipt",
	},
	assignment.RoleVerification: {
		starts: []string{"<!-- issue-spec:accepted-verification-receipt version=1 -->"},
		end:    "<!-- /issue-spec:accepted-verification-receipt -->",
		token:  "issue-spec:accepted-verification-receipt",
	},
}

// ObserveAcceptedReceiptAuthority reads only the common immutable identity
// fields from one role-specific compact marker. It does not accept a receipt
// or interpret any role result content. Review versions 1 and 2 are the only
// dual-version marker set; the other role marker contracts remain version 1.
func ObserveAcceptedReceiptAuthority(body string, role assignment.Role) (AcceptedReceiptAuthority, bool, error) {
	marker, ok := acceptedReceiptMarkers[role]
	if !ok {
		return AcceptedReceiptAuthority{}, false, fmt.Errorf("unsupported accepted receipt role %q", role)
	}
	body = preview.SemanticView(body)
	if !strings.Contains(body, marker.token) {
		return AcceptedReceiptAuthority{}, false, nil
	}
	startMarker, startCount := "", 0
	for _, candidate := range marker.starts {
		count := strings.Count(body, candidate)
		startCount += count
		if count == 1 {
			startMarker = candidate
		}
	}
	if startCount != 1 || strings.Count(body, marker.end) != 1 || strings.Count(body, marker.token) != 2 {
		return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt must contain exactly one recognized marker pair")
	}
	start, end := strings.Index(body, startMarker), strings.Index(body, marker.end)
	if end <= start {
		return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt marker order is invalid")
	}
	rawBlock := body[start+len(startMarker) : end]
	if len(rawBlock) < 3 || rawBlock[0] != '\n' || rawBlock[len(rawBlock)-1] != '\n' {
		return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt payload framing is invalid")
	}
	raw := []byte(rawBlock[1 : len(rawBlock)-1])
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil || !bytes.Equal(compact.Bytes(), raw) {
		return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt payload is not compact JSON")
	}
	fields, err := decodeAcceptedReceiptIdentity(raw)
	if err != nil {
		return AcceptedReceiptAuthority{}, true, err
	}
	authority := AcceptedReceiptAuthority{Role: role}
	if err := json.Unmarshal(fields["receipt_id"], &authority.ReceiptID); err != nil {
		return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt receipt_id is invalid")
	}
	if err := json.Unmarshal(fields["receipt_digest"], &authority.Digest); err != nil {
		return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt receipt_digest is invalid")
	}
	// Existing compact carriers call this field assignment_generation. The
	// projection schema shortens only its own input field to generation.
	if err := json.Unmarshal(fields["assignment_generation"], &authority.Generation); err != nil {
		return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt assignment_generation is invalid")
	}
	if !acceptedReceiptIDRe.MatchString(authority.ReceiptID) ||
		!acceptedReceiptDigestRe.MatchString(authority.Digest) || authority.Generation == 0 {
		return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt immutable identity is invalid")
	}
	if role == assignment.RoleReview || role == assignment.RoleVerification {
		hasID, hasDigest := len(fields["assignment_id"]) != 0, len(fields["assignment_digest"]) != 0
		if hasID != hasDigest {
			return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt assignment identity is incomplete")
		}
		if hasID {
			if err := json.Unmarshal(fields["assignment_id"], &authority.AssignmentID); err != nil ||
				!acceptedReceiptIDRe.MatchString(authority.AssignmentID) {
				return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt assignment_id is invalid")
			}
			if err := json.Unmarshal(fields["assignment_digest"], &authority.AssignmentDigest); err != nil ||
				!acceptedReceiptDigestRe.MatchString(authority.AssignmentDigest) {
				return AcceptedReceiptAuthority{}, true, errors.New("accepted receipt assignment_digest is invalid")
			}
		}
	}
	return authority, true, nil
}

func decodeAcceptedReceiptIdentity(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("accepted receipt payload must be one JSON object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("accepted receipt payload is invalid")
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("accepted receipt payload field name is invalid")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("accepted receipt payload has duplicate field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("accepted receipt payload field %q is invalid", name)
		}
		fields[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("accepted receipt payload is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("accepted receipt payload has trailing JSON")
	}
	for _, required := range []string{"receipt_id", "receipt_digest", "assignment_generation"} {
		if len(fields[required]) == 0 {
			return nil, fmt.Errorf("accepted receipt payload is missing %s", required)
		}
	}
	return fields, nil
}

func ParseTypedComment(body string) TypedComment {
	tc := TypedComment{Links: map[string][]string{}, Body: body}
	semanticBody := preview.SemanticView(CanonicalView(body))
	marker, hasMarker, err := findMarker(semanticBody)
	if err != nil {
		tc.Errors = append(tc.Errors, err.Error())
	}
	if hasMarker {
		tc.Marker = marker
		tc.Type = marker.Type
		tc.ID = marker.ID
	}

	lines := strings.Split(semanticBody, "\n")
	inLinks := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<!--") || trimmed == "" {
			if inLinks && trimmed == "" {
				break
			}
			continue
		}
		if inLinks {
			if strings.HasPrefix(trimmed, "- ") {
				name, value, ok := parseLinkLine(trimmed)
				if ok {
					tc.Links[name] = splitLinkValues(value)
					continue
				}
			}
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			if tc.HasHead {
				break
			}
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "Agent":
			tc.Agent = value
			tc.HasHead = true
		case "Agent Session ID":
			tc.AgentSessionID = value
			tc.HasHead = true
		case "Agent Session Source":
			tc.AgentSessionSource = value
			tc.HasHead = true
		case "Subject Revision":
			tc.SubjectRevision = value
			tc.HasHead = true
		case "Type":
			if tc.Type != "" && tc.Type != strings.ToUpper(value) {
				tc.Errors = append(tc.Errors, fmt.Sprintf("marker type %s does not match header type %s", tc.Type, value))
			}
			tc.Type = strings.ToUpper(value)
			tc.HasHead = true
		case "ID":
			if tc.ID != "" && tc.ID != value {
				tc.Errors = append(tc.Errors, fmt.Sprintf("marker id %s does not match header id %s", tc.ID, value))
			}
			tc.ID = value
			tc.HasHead = true
		case "Status":
			tc.Status = value
			tc.HasHead = true
		case "Scope":
			tc.Scope = value
			tc.HasHead = true
		case "Links":
			inLinks = true
			tc.HasHead = true
		default:
			if tc.HasHead {
				break
			}
		}
	}

	if tc.Type != "" && !AllowedTypes[tc.Type] {
		tc.Errors = append(tc.Errors, fmt.Sprintf("unsupported type %s", tc.Type))
	}
	if tc.ID != "" && !idRe.MatchString(tc.ID) {
		tc.Errors = append(tc.Errors, fmt.Sprintf("invalid id %s", tc.ID))
	}
	if tc.Status != "" && !AllowedStatuses[tc.Status] {
		tc.Errors = append(tc.Errors, fmt.Sprintf("unsupported status %s", tc.Status))
	}
	if hasMarker && !tc.HasHead {
		tc.Errors = append(tc.Errors, "typed comment is missing visible header")
	}
	if tc.Type == "PROCESS" {
		input, explicit, err := parseProcessAssignment(semanticBody)
		if err != nil {
			tc.Errors = append(tc.Errors, err.Error())
		} else if explicit {
			tc.Assignment = &input
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"Agent", tc.Agent},
		{"Type", tc.Type},
		{"ID", tc.ID},
		{"Status", tc.Status},
		{"Scope", tc.Scope},
	} {
		if hasMarker && strings.TrimSpace(field.value) == "" {
			tc.Errors = append(tc.Errors, "typed comment is missing "+field.name)
		}
	}
	return tc
}

func parseProcessAssignment(body string) (assignment.ProcessInput, bool, error) {
	sections := markdownSectionContents(LogicalBody(body), "### Assignment")
	if len(sections) == 0 {
		return assignment.ProcessInput{}, false, nil
	}
	if len(sections) != 1 {
		return assignment.ProcessInput{}, true, errors.New("PROCESS has multiple `### Assignment` sections")
	}
	section := strings.TrimSpace(sections[0])
	if !strings.HasPrefix(section, "```json\n") || !strings.HasSuffix(section, "\n```") {
		return assignment.ProcessInput{}, true, errors.New("`### Assignment` must contain exactly one fenced `json` object")
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(section, "```json\n"), "\n```")
	if strings.TrimSpace(payload) == "" || strings.Contains(payload, "```") {
		return assignment.ProcessInput{}, true, errors.New("`### Assignment` must contain exactly one fenced `json` object")
	}
	input, err := assignment.ParseProcessInputJSON([]byte(payload))
	if err != nil {
		return assignment.ProcessInput{}, true, err
	}
	return input, true, nil
}

func EnsureTypedBody(commentType, id, body string, opts BodyOptions) (string, error) {
	commentType = strings.ToUpper(strings.TrimSpace(commentType))
	id = strings.TrimSpace(id)
	if !AllowedTypes[commentType] {
		return "", fmt.Errorf("unsupported type %s", commentType)
	}
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("invalid id %s", id)
	}
	if opts.Agent == "" {
		opts.Agent = "Coordinator"
	}
	if opts.Status == "" {
		opts.Status = "draft"
	}
	if opts.Scope == "" {
		opts.Scope = "N/A"
	}
	if !AllowedStatuses[opts.Status] {
		return "", fmt.Errorf("unsupported status %s", opts.Status)
	}

	tc := ParseTypedComment(body)
	if tc.Marker.Type != "" && (tc.Marker.Type != commentType || tc.Marker.ID != id) {
		return "", fmt.Errorf("body marker is %s/%s, command requested %s/%s", tc.Marker.Type, tc.Marker.ID, commentType, id)
	}
	if tc.HasHead {
		if tc.Type != "" && tc.Type != commentType {
			return "", fmt.Errorf("body header type is %s, command requested %s", tc.Type, commentType)
		}
		if tc.ID != "" && tc.ID != id {
			return "", fmt.Errorf("body header id is %s, command requested %s", tc.ID, id)
		}
		if len(tc.Errors) > 0 {
			return "", errors.New(strings.Join(tc.Errors, "; "))
		}
		if !HasTypedMarker(body) {
			body = RenderMarker(commentType, id, 1) + "\n" + strings.TrimLeft(body, "\n")
		}
		if opts.AgentSessionID != "" || opts.AgentSessionSource != "" {
			return StampTypedSessionMetadata(body, opts.AgentSessionID, opts.AgentSessionSource)
		}
		return body, nil
	}

	content := strings.TrimSpace(body)
	if content == "" {
		content = "## Summary\n\nTBD"
	}
	return RenderMarker(commentType, id, 1) + "\n" + RenderHeader(commentType, id, opts) + "\n\n" + content + "\n", nil
}

func RenderHeader(commentType, id string, opts BodyOptions) string {
	links := defaultLinks(opts.Links)
	keys := []string{"Proposal Issue", "Design Issue", "Implement Issue", "Related Comments", "PR"}
	var b strings.Builder
	fmt.Fprintf(&b, "Agent: %s\n", valueOr(opts.Agent, "Coordinator"))
	if strings.TrimSpace(opts.AgentSessionID) != "" {
		fmt.Fprintf(&b, "Agent Session ID: %s\n", strings.TrimSpace(opts.AgentSessionID))
	}
	if strings.TrimSpace(opts.AgentSessionSource) != "" {
		fmt.Fprintf(&b, "Agent Session Source: %s\n", strings.TrimSpace(opts.AgentSessionSource))
	}
	if strings.TrimSpace(opts.SubjectRevision) != "" {
		fmt.Fprintf(&b, "Subject Revision: %s\n", strings.TrimSpace(opts.SubjectRevision))
	}
	fmt.Fprintf(&b, "Type: %s\n", strings.ToUpper(commentType))
	fmt.Fprintf(&b, "ID: %s\n", id)
	fmt.Fprintf(&b, "Status: %s\n", valueOr(opts.Status, "draft"))
	fmt.Fprintf(&b, "Scope: %s\n", valueOr(opts.Scope, "N/A"))
	b.WriteString("Links:\n")
	for _, key := range keys {
		values := links[key]
		if len(values) == 0 {
			values = []string{"N/A"}
		}
		fmt.Fprintf(&b, "- %s: %s\n", key, strings.Join(values, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func StampTypedSessionMetadata(body, sessionID, sessionSource string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	sessionSource = strings.TrimSpace(sessionSource)
	if sessionID == "" && sessionSource == "" {
		return body, nil
	}
	tc := ParseTypedComment(body)
	if !tc.HasHead {
		return "", errors.New("typed comment is missing visible header")
	}
	if len(tc.Errors) > 0 {
		return "", errors.New(strings.Join(tc.Errors, "; "))
	}
	lines := strings.Split(body, "\n")
	semanticLines := strings.Split(preview.SemanticView(body), "\n")
	agentIndex := -1
	sessionIDIndex := -1
	sessionSourceIndex := -1
	for i, line := range semanticLines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Agent:"):
			agentIndex = i
		case strings.HasPrefix(trimmed, "Agent Session ID:"):
			sessionIDIndex = i
		case strings.HasPrefix(trimmed, "Agent Session Source:"):
			sessionSourceIndex = i
		case strings.HasPrefix(trimmed, "Type:"):
			if agentIndex == -1 {
				return "", errors.New("typed comment is missing Agent")
			}
			if sessionSource != "" {
				lines = upsertHeaderLine(lines, &sessionSourceIndex, sessionIDIndex, agentIndex+1, "Agent Session Source: "+sessionSource)
			}
			if sessionID != "" {
				lines = upsertHeaderLine(lines, &sessionIDIndex, agentIndex, agentIndex+1, "Agent Session ID: "+sessionID)
			}
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", errors.New("typed comment is missing Type header")
}

func upsertHeaderLine(lines []string, index *int, afterIndex, fallbackIndex int, line string) []string {
	if *index >= 0 {
		lines[*index] = line
		return lines
	}
	insertAt := fallbackIndex
	if afterIndex >= 0 {
		insertAt = afterIndex + 1
	}
	if insertAt < 0 {
		insertAt = 0
	}
	if insertAt > len(lines) {
		insertAt = len(lines)
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:])
	lines[insertAt] = line
	*index = insertAt
	return lines
}

func IsLikelyTyped(body string) bool {
	semanticBody := preview.SemanticView(body)
	return HasTypedMarker(semanticBody) || (strings.Contains(semanticBody, "Type:") && strings.Contains(semanticBody, "ID:") && strings.Contains(semanticBody, "Status:"))
}

func NormalizeURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func RelatedCommentURLs(tc TypedComment) []string {
	return filterURLValues(tc.Links["Related Comments"])
}

func LinkValues(tc TypedComment, name string) []string {
	return filterURLValues(tc.Links[name])
}

func parseLinkLine(line string) (string, string, bool) {
	line = strings.TrimPrefix(strings.TrimSpace(line), "- ")
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(name), strings.TrimSpace(value), true
}

func splitLinkValues(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "N/A") {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && !strings.EqualFold(part, "N/A") {
			out = append(out, part)
		}
	}
	return out
}

func filterURLValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.EqualFold(value, "N/A") {
			continue
		}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func defaultLinks(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, key := range []string{"Proposal Issue", "Design Issue", "Implement Issue", "Related Comments", "PR"} {
		out[key] = []string{"N/A"}
	}
	for key, values := range in {
		if len(values) == 0 {
			continue
		}
		out[key] = values
	}
	return out
}

func visibleMetadata(body string) map[string]string {
	body = preview.SemanticView(body)
	out := map[string]string{}
	started := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<!--") {
			continue
		}
		if trimmed == "" {
			if started {
				break
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			if started {
				break
			}
			continue
		}
		started = true
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
