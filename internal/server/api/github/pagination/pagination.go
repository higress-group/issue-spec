// Package pagination implements the bounded GitHub-compatible page contract.
package pagination

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

const (
	DefaultPerPage = 30
	MaximumPerPage = 100
)

// Options is the normalized collection cursor accepted by handlers.
type Options struct {
	Page    int
	PerPage int
	Since   *time.Time
}

// FieldError can be translated directly into a GitHub validation envelope.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ParseError reports all invalid pagination fields in deterministic order.
type ParseError struct {
	Fields []FieldError
}

func (e *ParseError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, field := range e.Fields {
		parts = append(parts, field.Field+": "+field.Message)
	}
	return "invalid pagination: " + strings.Join(parts, "; ")
}

// Parser permits endpoints to select a smaller default while retaining the
// same hard upper bound.
type Parser struct {
	DefaultPerPage int
	MaximumPerPage int
}

// Parse applies the shared default and hard maximum.
func Parse(values url.Values) (Options, error) {
	return (Parser{DefaultPerPage: DefaultPerPage, MaximumPerPage: MaximumPerPage}).Parse(values)
}

// Parse validates page, per_page and since. Ambiguous repeated parameters are
// rejected instead of silently selecting an attacker-controlled value.
func (p Parser) Parse(values url.Values) (Options, error) {
	if p.DefaultPerPage <= 0 {
		p.DefaultPerPage = DefaultPerPage
	}
	if p.MaximumPerPage <= 0 {
		p.MaximumPerPage = MaximumPerPage
	}
	if p.DefaultPerPage > p.MaximumPerPage {
		return Options{}, errors.New("pagination parser default exceeds maximum")
	}
	result := Options{Page: 1, PerPage: p.DefaultPerPage}
	var fields []FieldError

	if raw, ok := single(values, "page", &fields); ok && raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			fields = append(fields, FieldError{Field: "page", Code: "invalid", Message: "must be a positive integer"})
		} else {
			result.Page = value
		}
	}
	if raw, ok := single(values, "per_page", &fields); ok && raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > p.MaximumPerPage {
			fields = append(fields, FieldError{Field: "per_page", Code: "invalid", Message: fmt.Sprintf("must be between 1 and %d", p.MaximumPerPage)})
		} else {
			result.PerPage = value
		}
	}
	if raw, ok := single(values, "since", &fields); ok && raw != "" {
		value, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			fields = append(fields, FieldError{Field: "since", Code: "invalid", Message: "must be an RFC3339 timestamp"})
		} else {
			value = value.UTC()
			result.Since = &value
		}
	}
	if len(fields) > 0 {
		return Options{}, &ParseError{Fields: fields}
	}
	return result, nil
}

func single(values url.Values, key string, fields *[]FieldError) (string, bool) {
	items, ok := values[key]
	if !ok {
		return "", false
	}
	if len(items) != 1 {
		*fields = append(*fields, FieldError{Field: key, Code: "invalid", Message: "must be specified exactly once"})
		return "", false
	}
	return items[0], true
}

// BuildLinkHeader constructs canonical RFC5988 links in a fixed relation order
// while preserving every non-page filter (including repeated filters).
func BuildLinkHeader(origin publicurl.Origin, path string, filters url.Values, currentPage, perPage, totalItems int) (string, error) {
	if currentPage < 1 || perPage < 1 || totalItems < 0 {
		return "", errors.New("page, per_page and total item values are out of range")
	}
	lastPage := (totalItems + perPage - 1) / perPage
	if lastPage <= 1 {
		return "", nil
	}
	if currentPage > lastPage {
		currentPage = lastPage
	}
	type relation struct {
		name string
		page int
	}
	var relations []relation
	if currentPage > 1 {
		relations = append(relations, relation{"first", 1}, relation{"prev", currentPage - 1})
	}
	if currentPage < lastPage {
		relations = append(relations, relation{"next", currentPage + 1}, relation{"last", lastPage})
	}
	links := make([]string, 0, len(relations))
	for _, rel := range relations {
		query := cloneValues(filters)
		query.Set("page", strconv.Itoa(rel.page))
		query.Set("per_page", strconv.Itoa(perPage))
		absolute, err := origin.URL(path, query)
		if err != nil {
			return "", err
		}
		links = append(links, fmt.Sprintf("<%s>; rel=\"%s\"", absolute, rel.name))
	}
	return strings.Join(links, ", "), nil
}

func cloneValues(values url.Values) url.Values {
	result := make(url.Values, len(values)+2)
	for key, items := range values {
		if key == "page" || key == "per_page" {
			continue
		}
		result[key] = append([]string(nil), items...)
	}
	return result
}
