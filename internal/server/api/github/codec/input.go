package codec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	MaximumJSONBytes       = 1 << 20
	MaximumIssueTitleRunes = 256
	MaximumBodyBytes       = 1 << 20
	MaximumLabelNameRunes  = 50
	MaximumLabelDescRunes  = 100
)

var labelColorPattern = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

type Violation struct {
	Resource string
	Field    string
	Code     string
	Message  string
}

type CreateIssueInput struct {
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

func (i CreateIssueInput) Validate() []Violation {
	var result []Violation
	result = append(result, validateRequiredText("Issue", "title", i.Title, MaximumIssueTitleRunes)...)
	result = append(result, validateBody("Issue", i.Body)...)
	result = append(result, validateLabels(i.Labels)...)
	return result
}

type UpdateIssueInput struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State *string `json:"state,omitempty"`
}

func (i UpdateIssueInput) Validate() []Violation {
	var result []Violation
	if i.Title == nil && i.Body == nil && i.State == nil {
		return []Violation{{Resource: "Issue", Field: "request", Code: "missing_field", Message: "at least one mutable field is required"}}
	}
	if i.Title != nil {
		result = append(result, validateRequiredText("Issue", "title", *i.Title, MaximumIssueTitleRunes)...)
	}
	if i.Body != nil {
		result = append(result, validateBody("Issue", *i.Body)...)
	}
	if i.State != nil && *i.State != "open" && *i.State != "closed" {
		result = append(result, Violation{Resource: "Issue", Field: "state", Code: "invalid", Message: "must be open or closed"})
	}
	return result
}

type CommentInput struct {
	Body *string `json:"body"`
}

func (i CommentInput) Validate() []Violation {
	if i.Body == nil {
		return []Violation{{Resource: "IssueComment", Field: "body", Code: "missing_field", Message: "is required"}}
	}
	return validateBody("IssueComment", *i.Body)
}

type CreateLabelInput struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

func (i CreateLabelInput) Validate() []Violation {
	result := validateRequiredText("Label", "name", i.Name, MaximumLabelNameRunes)
	if i.Color != "" && !labelColorPattern.MatchString(strings.TrimPrefix(i.Color, "#")) {
		result = append(result, Violation{Resource: "Label", Field: "color", Code: "invalid", Message: "must be a six-digit hexadecimal color"})
	}
	if utf8.RuneCountInString(i.Description) > MaximumLabelDescRunes {
		result = append(result, Violation{Resource: "Label", Field: "description", Code: "custom", Message: fmt.Sprintf("is longer than %d characters", MaximumLabelDescRunes)})
	}
	return result
}

type LabelsInput struct {
	Labels []string `json:"labels"`
}

func (i LabelsInput) Validate() []Violation { return validateLabels(i.Labels) }

type ReactionInput struct {
	Content string `json:"content"`
}

func (i ReactionInput) Validate() []Violation {
	switch i.Content {
	case "+1", "-1", "laugh", "confused", "heart", "hooray", "rocket", "eyes":
		return nil
	default:
		return []Violation{{Resource: "Reaction", Field: "content", Code: "invalid", Message: "is not a supported reaction"}}
	}
}

// DecodeJSON bounds the request and rejects trailing JSON. Unknown fields are
// tolerated for GitHub forward compatibility; semantic validation remains
// explicit on each input type.
func DecodeJSON(reader io.Reader, destination any) error {
	if reader == nil {
		return errors.New("request body is required")
	}
	limited := &io.LimitedReader{R: reader, N: MaximumJSONBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("request body exceeds %d bytes", MaximumJSONBytes)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func validateRequiredText(resource, field, value string, max int) []Violation {
	if strings.TrimSpace(value) == "" {
		return []Violation{{Resource: resource, Field: field, Code: "missing_field", Message: "is required"}}
	}
	if utf8.RuneCountInString(value) > max {
		return []Violation{{Resource: resource, Field: field, Code: "custom", Message: fmt.Sprintf("is longer than %d characters", max)}}
	}
	return nil
}

func validateBody(resource, body string) []Violation {
	if len(body) > MaximumBodyBytes {
		return []Violation{{Resource: resource, Field: "body", Code: "custom", Message: fmt.Sprintf("is larger than %d bytes", MaximumBodyBytes)}}
	}
	if !utf8.ValidString(body) {
		return []Violation{{Resource: resource, Field: "body", Code: "invalid", Message: "must contain valid UTF-8"}}
	}
	return nil
}

func validateLabels(labels []string) []Violation {
	seen := make(map[string]struct{}, len(labels))
	var result []Violation
	for _, label := range labels {
		if violations := validateRequiredText("Issue", "labels", label, MaximumLabelNameRunes); len(violations) > 0 {
			result = append(result, violations...)
			continue
		}
		key := strings.ToLower(label)
		if _, exists := seen[key]; exists {
			result = append(result, Violation{Resource: "Issue", Field: "labels", Code: "invalid", Message: "contains a duplicate label"})
		}
		seen[key] = struct{}{}
	}
	return result
}
