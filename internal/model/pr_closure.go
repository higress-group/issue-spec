package model

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	prIssueClosureStart = "<!-- issue-spec:pr-close-issues version=1 -->"
	prIssueClosureEnd   = "<!-- /issue-spec:pr-close-issues -->"
)

type IssueClosureRef struct {
	Kind   string
	Number int
}

func AddIssueClosureBlock(body string, refs []IssueClosureRef) (string, bool, error) {
	block, err := renderIssueClosureBlock(refs)
	if err != nil {
		return "", false, err
	}
	start := strings.Index(body, prIssueClosureStart)
	end := strings.Index(body, prIssueClosureEnd)
	if start >= 0 || end >= 0 {
		if start < 0 || end < 0 || end < start {
			return "", false, fmt.Errorf("malformed issue-spec PR closing block")
		}
		end += len(prIssueClosureEnd)
		updated := body[:start] + block + body[end:]
		return updated, updated != body, nil
	}
	if strings.TrimSpace(body) == "" {
		return block + "\n", true, nil
	}
	updated := strings.TrimRight(body, "\n") + "\n\n" + block + "\n"
	return updated, updated != body, nil
}

func VerifyIssueClosureBlock(body string, refs []IssueClosureRef) error {
	block, err := findIssueClosureBlock(body)
	if err != nil {
		return err
	}
	expected := map[int]IssueClosureRef{}
	for _, ref := range refs {
		if ref.Number <= 0 {
			return fmt.Errorf("%s issue number must be positive", ref.Kind)
		}
		expected[ref.Number] = ref
	}
	found := map[int]bool{}
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Closes #") {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(trimmed, "Closes #")))
		if err != nil {
			return fmt.Errorf("malformed issue-spec PR closing link %q", trimmed)
		}
		if _, ok := expected[number]; !ok {
			return fmt.Errorf("unexpected issue closing link Closes #%d", number)
		}
		found[number] = true
	}
	for _, ref := range refs {
		if !found[ref.Number] {
			return fmt.Errorf("missing %s issue closing link Closes #%d", ref.Kind, ref.Number)
		}
	}
	return nil
}

func findIssueClosureBlock(body string) (string, error) {
	start := strings.Index(body, prIssueClosureStart)
	end := strings.Index(body, prIssueClosureEnd)
	if start < 0 && end < 0 {
		return "", fmt.Errorf("missing issue-spec PR closing block")
	}
	if start < 0 || end < 0 || end < start {
		return "", fmt.Errorf("malformed issue-spec PR closing block")
	}
	return body[start : end+len(prIssueClosureEnd)], nil
}

func renderIssueClosureBlock(refs []IssueClosureRef) (string, error) {
	if len(refs) == 0 {
		return "", fmt.Errorf("at least one issue closure ref is required")
	}
	var b strings.Builder
	b.WriteString(prIssueClosureStart)
	b.WriteString("\nIssue-spec managed closing links:\n")
	for _, ref := range refs {
		if ref.Number <= 0 {
			return "", fmt.Errorf("%s issue number must be positive", ref.Kind)
		}
		fmt.Fprintf(&b, "Closes #%d\n", ref.Number)
	}
	b.WriteString(prIssueClosureEnd)
	return b.String(), nil
}
