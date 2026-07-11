package errors

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

const ProblemTypeBase = "https://issue-spec.dev/problems/"

// Problem is the native application/problem+json contract.
type Problem struct {
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Status    int            `json:"status"`
	Detail    string         `json:"detail,omitempty"`
	Instance  string         `json:"instance,omitempty"`
	Code      string         `json:"code"`
	RequestID string         `json:"request_id"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// UnsupportedCapabilityError is the stable programmatic error returned before
// a self-hosted backend attempts a pull, review, status, or check operation.
type UnsupportedCapabilityError struct {
	Operation  string
	Capability string
}

func (e *UnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("operation %q requires unsupported backend capability %q", e.Operation, e.Capability)
}

// AsProblem converts the programmatic error to the native wire contract.
func (e *UnsupportedCapabilityError) AsProblem(requestID string) Problem {
	return UnsupportedCapability(e.Operation, e.Capability, requestID)
}

// RequireCapability returns an actionable stable error when supported is
// false. Callers invoke it before any partial workflow state is written.
func RequireCapability(operation, capability string, supported bool) error {
	if supported {
		return nil
	}
	return &UnsupportedCapabilityError{Operation: operation, Capability: capability}
}

// NewProblem builds a problem using a stable code and a generated request ID
// when middleware has not supplied one.
func NewProblem(status int, code, title, detail, requestID string) Problem {
	if requestID == "" {
		requestID = NewRequestID()
	}
	return Problem{Type: ProblemTypeBase + code, Title: title, Status: status, Detail: detail, Code: code, RequestID: requestID}
}

// UnsupportedCapability is actionable and names only public operation data.
func UnsupportedCapability(operation, capability, requestID string) Problem {
	detail := fmt.Sprintf("operation %q requires capability %q; configure a code evidence provider or use a GitHub profile", operation, capability)
	problem := NewProblem(http.StatusNotImplemented, "unsupported_capability", "Backend capability is not available", detail, requestID)
	problem.Meta = map[string]any{"operation": operation, "capability": capability, "action": "configure_code_evidence_provider_or_github_profile"}
	return problem
}

// WriteProblem emits the native media type and stable request identity.
func WriteProblem(w http.ResponseWriter, problem Problem) {
	if problem.RequestID == "" {
		problem.RequestID = NewRequestID()
	}
	if problem.Type == "" {
		problem.Type = ProblemTypeBase + problem.Code
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-ID", problem.RequestID)
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}

func NewRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(value[:])
}

var (
	bearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+`)
	secretPattern = regexp.MustCompile(`(?i)\b(token|password|secret|api[_-]?key)=([^\s&;,]+)`)
)

// Redactor removes configured secret values, tenant identifiers and common
// credential syntax from diagnostic text before it reaches a response or log.
type Redactor struct {
	values []string
}

func NewRedactor(secrets, tenantIdentifiers []string) Redactor {
	values := append(append([]string(nil), secrets...), tenantIdentifiers...)
	values = compact(values)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return Redactor{values: values}
}

func (r Redactor) Text(value string) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = secretPattern.ReplaceAllString(value, "$1=[REDACTED]")
	for _, secret := range r.values {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func (r Redactor) Problem(problem Problem) Problem {
	problem.Detail = r.Text(problem.Detail)
	problem.Instance = r.Text(problem.Instance)
	for key, value := range problem.Meta {
		if text, ok := value.(string); ok {
			problem.Meta[key] = r.Text(text)
		}
	}
	return problem
}

func compact(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
