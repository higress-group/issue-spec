package model

import (
	"errors"
	"fmt"
	"strings"
)

type typedHeaderRelationship struct {
	name   string
	values []string
	index  int
}

type typedHeaderRelationships struct {
	lines    []string
	blockEnd int
	ordered  []typedHeaderRelationship
	byName   map[string]typedHeaderRelationship
	comment  TypedComment
}

// MergeTypedHeaderRelationships adds every relationship carried by before's
// typed Links header to desired. Existing desired relationships keep their
// order and spelling; missing values and future relationship header entries
// are appended. The operation is additive because the v1 reconcile protocol
// has no relationship-removal operation.
func MergeTypedHeaderRelationships(before, desired string) (string, bool, error) {
	existing, err := parseTypedHeaderRelationships(before)
	if err != nil {
		return "", false, fmt.Errorf("parse existing typed relationships: %w", err)
	}
	next, err := parseTypedHeaderRelationships(desired)
	if err != nil {
		return "", false, fmt.Errorf("parse desired typed relationships: %w", err)
	}
	if existing.comment.Type != next.comment.Type || existing.comment.ID != next.comment.ID {
		return "", false, fmt.Errorf("typed relationship merge identity mismatch: existing=%s/%s desired=%s/%s",
			existing.comment.Type, existing.comment.ID, next.comment.Type, next.comment.ID)
	}

	changed := false
	var additions []string
	for _, relationship := range existing.ordered {
		if len(relationship.values) == 0 {
			continue
		}
		desiredRelationship, found := next.byName[relationship.name]
		if !found {
			additions = append(additions, fmt.Sprintf("- %s: %s", relationship.name, strings.Join(relationship.values, ", ")))
			changed = true
			continue
		}
		values := append([]string(nil), desiredRelationship.values...)
		have := map[string]bool{}
		for _, value := range values {
			have[NormalizeURL(value)] = true
		}
		for _, value := range relationship.values {
			normalized := NormalizeURL(value)
			if !have[normalized] {
				values = append(values, value)
				have[normalized] = true
				changed = true
			}
		}
		if len(values) > 0 && !sameLinkValues(values, desiredRelationship.values) {
			next.lines[desiredRelationship.index] = fmt.Sprintf("- %s: %s", desiredRelationship.name, strings.Join(values, ", "))
		}
	}
	if len(additions) > 0 {
		next.lines = append(next.lines[:next.blockEnd], append(additions, next.lines[next.blockEnd:]...)...)
	}
	if !changed {
		return desired, false, nil
	}
	return strings.Join(next.lines, "\n"), true, nil
}

func parseTypedHeaderRelationships(body string) (typedHeaderRelationships, error) {
	comment := ParseTypedComment(body)
	if !HasTypedMarker(body) || !comment.HasHead {
		return typedHeaderRelationships{}, errors.New("typed marker and visible header are required")
	}
	if len(comment.Errors) > 0 {
		return typedHeaderRelationships{}, errors.New(strings.Join(comment.Errors, "; "))
	}
	result := typedHeaderRelationships{lines: strings.Split(body, "\n"), byName: map[string]typedHeaderRelationship{}, comment: comment}
	linksIndex := -1
	for index, line := range result.lines {
		if strings.TrimSpace(line) == "Links:" {
			linksIndex = index
			break
		}
	}
	if linksIndex < 0 {
		return typedHeaderRelationships{}, errors.New("typed comment is missing Links block")
	}
	result.blockEnd = linksIndex + 1
	for result.blockEnd < len(result.lines) {
		line := strings.TrimSpace(result.lines[result.blockEnd])
		if !strings.HasPrefix(line, "- ") {
			break
		}
		name, value, ok := parseLinkLine(line)
		if !ok || name == "" {
			return typedHeaderRelationships{}, fmt.Errorf("typed comment has invalid relationship line %q", line)
		}
		if _, duplicate := result.byName[name]; duplicate {
			return typedHeaderRelationships{}, fmt.Errorf("typed comment has duplicate relationship header %q", name)
		}
		relationship := typedHeaderRelationship{name: name, values: splitLinkValues(value), index: result.blockEnd}
		result.ordered = append(result.ordered, relationship)
		result.byName[name] = relationship
		result.blockEnd++
	}
	return result, nil
}

func sameLinkValues(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// TypedSectionList returns the non-empty bullet values in one exact level-three
// section of a typed artifact body. N/A is the canonical empty sentinel. The
// function deliberately ignores relationship headers: planning authority lives
// in the visible TASK/PROCESS sections, while legacy Links remain navigation.
func TypedSectionList(body, heading string) []string {
	heading = strings.TrimSpace(heading)
	if heading == "" {
		return nil
	}
	if !strings.HasPrefix(heading, "### ") {
		heading = "### " + heading
	}
	var values []string
	inSection := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			inSection = strings.EqualFold(trimmed, heading)
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			inSection = false
			continue
		}
		if !inSection || !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if value == "" || strings.EqualFold(value, "N/A") {
			continue
		}
		values = append(values, value)
	}
	return values
}

func AddRelatedCommentLink(body, url string) (string, bool, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", false, fmt.Errorf("related comment URL is empty")
	}
	tc := ParseTypedComment(body)
	if len(tc.Errors) > 0 {
		return "", false, errors.New(strings.Join(tc.Errors, "; "))
	}
	lines := strings.Split(body, "\n")
	linksIndex := -1
	relatedIndex := -1
	linkBlockEnd := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Links:" {
			linksIndex = i
			linkBlockEnd = i + 1
			for j := i + 1; j < len(lines); j++ {
				next := strings.TrimSpace(lines[j])
				if strings.HasPrefix(next, "- ") {
					name, _, ok := parseLinkLine(next)
					if ok && name == "Related Comments" {
						relatedIndex = j
					}
					linkBlockEnd = j + 1
					continue
				}
				break
			}
			break
		}
	}
	if linksIndex == -1 {
		return "", false, fmt.Errorf("typed comment is missing Links block")
	}

	if relatedIndex >= 0 {
		name, value, _ := parseLinkLine(strings.TrimSpace(lines[relatedIndex]))
		values := splitLinkValues(value)
		for _, existing := range values {
			if NormalizeURL(existing) == NormalizeURL(url) {
				return body, false, nil
			}
		}
		values = append(values, url)
		lines[relatedIndex] = fmt.Sprintf("- %s: %s", name, strings.Join(values, ", "))
		return strings.Join(lines, "\n"), true, nil
	}

	newLine := "- Related Comments: " + url
	if linkBlockEnd <= linksIndex {
		linkBlockEnd = linksIndex + 1
	}
	lines = append(lines[:linkBlockEnd], append([]string{newLine}, lines[linkBlockEnd:]...)...)
	return strings.Join(lines, "\n"), true, nil
}
