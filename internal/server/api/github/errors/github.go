// Package errors writes compatible and native error envelopes without exposing
// internal tenant or credential details.
package errors

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
)

const documentationBase = "https://docs.github.com/rest"

// Field is the GitHub validation error shape used by client idempotency logic.
type Field struct {
	Resource string `json:"resource"`
	Field    string `json:"field"`
	Code     string `json:"code"`
	Message  string `json:"message,omitempty"`
}

// Envelope is a GitHub-compatible JSON error response.
type Envelope struct {
	Message          string  `json:"message"`
	Errors           []Field `json:"errors,omitempty"`
	DocumentationURL string  `json:"documentation_url"`
}

// GitHubError carries an HTTP status and safe public envelope.
type GitHubError struct {
	Status    int
	Envelope  Envelope
	Headers   http.Header
	RequestID string
}

func (e GitHubError) Error() string { return e.Envelope.Message }

func Unauthorized(requestID string) GitHubError {
	return GitHubError{
		Status: http.StatusUnauthorized, RequestID: requestID,
		Envelope: Envelope{Message: "Bad credentials", DocumentationURL: documentationBase},
		Headers:  http.Header{"Www-Authenticate": {`Bearer realm="issue-spec"`}},
	}
}

func Forbidden(requestID string) GitHubError {
	return GitHubError{Status: http.StatusForbidden, RequestID: requestID, Envelope: Envelope{Message: "Resource not accessible by personal access token", DocumentationURL: documentationBase}}
}

// NotFound intentionally accepts no resource detail. Missing and invisible
// resources therefore serialize byte-for-byte identically.
func NotFound(requestID string) GitHubError {
	return GitHubError{Status: http.StatusNotFound, RequestID: requestID, Envelope: Envelope{Message: "Not Found", DocumentationURL: documentationBase}}
}

func Validation(requestID string, violations []codec.Violation) GitHubError {
	fields := make([]Field, 0, len(violations))
	for _, violation := range violations {
		fields = append(fields, Field{Resource: violation.Resource, Field: violation.Field, Code: violation.Code, Message: violation.Message})
	}
	return GitHubError{Status: http.StatusUnprocessableEntity, RequestID: requestID, Envelope: Envelope{Message: "Validation Failed", Errors: fields, DocumentationURL: documentationBase}}
}

func PaginationValidation(requestID string, parseError *pagination.ParseError) GitHubError {
	fields := make([]Field, 0, len(parseError.Fields))
	for _, violation := range parseError.Fields {
		fields = append(fields, Field{Resource: "Pagination", Field: violation.Field, Code: violation.Code, Message: violation.Message})
	}
	return GitHubError{Status: http.StatusUnprocessableEntity, RequestID: requestID, Envelope: Envelope{Message: "Validation Failed", Errors: fields, DocumentationURL: documentationBase}}
}

// LabelAlreadyExists preserves the exact code used by the current client to
// turn duplicate label creation into an idempotent success.
func LabelAlreadyExists(requestID string) GitHubError {
	return GitHubError{
		Status: http.StatusUnprocessableEntity, RequestID: requestID,
		Envelope: Envelope{
			Message: "Validation Failed", DocumentationURL: documentationBase,
			Errors: []Field{{Resource: "Label", Field: "name", Code: "already_exists"}},
		},
	}
}

func TooManyRequests(requestID string, delay time.Duration) GitHubError {
	header := make(http.Header)
	pagination.SetRetryAfter(header, delay)
	return GitHubError{
		Status: http.StatusTooManyRequests, RequestID: requestID, Headers: header,
		Envelope: Envelope{Message: "API rate limit exceeded", DocumentationURL: documentationBase},
	}
}

// WriteGitHub writes one stable JSON object and never includes an internal
// error string.
func WriteGitHub(w http.ResponseWriter, apiError GitHubError) {
	copyHeaders(w.Header(), apiError.Headers)
	if apiError.RequestID == "" {
		apiError.RequestID = NewRequestID()
	}
	w.Header().Set("X-Request-ID", apiError.RequestID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(apiError.Status)
	_ = json.NewEncoder(w).Encode(apiError.Envelope)
}

func copyHeaders(destination, source http.Header) {
	for name, values := range source {
		destination.Del(name)
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

// RetryAfterSeconds returns a parsed Retry-After value for tests and adapters.
func RetryAfterSeconds(apiError GitHubError) int {
	value, _ := strconv.Atoi(apiError.Headers.Get("Retry-After"))
	return value
}
