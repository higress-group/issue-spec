package model

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrProcessPRLinkConflict = errors.New("PROCESS PR link already identifies a different code change")

func AddPRLink(body, url string) (string, bool, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", false, fmt.Errorf("PR URL is empty")
	}
	tc := ParseTypedComment(body)
	if len(tc.Errors) > 0 {
		return "", false, errors.New(strings.Join(tc.Errors, "; "))
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- PR:") {
			continue
		}
		name, value, ok := parseLinkLine(trimmed)
		if !ok {
			return "", false, fmt.Errorf("malformed PR link line")
		}
		values := splitLinkValues(value)
		for _, existing := range values {
			if NormalizeURL(existing) == NormalizeURL(url) {
				return body, false, nil
			}
		}
		values = append(values, url)
		lines[i] = fmt.Sprintf("- %s: %s", name, strings.Join(values, ", "))
		return strings.Join(lines, "\n"), true, nil
	}
	return "", false, fmt.Errorf("typed comment is missing PR link line")
}

// SetProcessCodeChangeLink fills the single PROCESS PR slot from a trusted
// provider-neutral code-change relationship. Unlike AddPRLink, it never
// appends or replaces an existing non-empty identity.
func SetProcessCodeChangeLink(body, processID, canonicalURL string) (string, bool, error) {
	processID = strings.TrimSpace(processID)
	canonicalURL = strings.TrimSpace(canonicalURL)
	parsedURL, urlErr := url.Parse(canonicalURL)
	if urlErr != nil || canonicalURL == "" || parsedURL.Scheme != "https" || parsedURL.Host == "" ||
		parsedURL.User != nil || parsedURL.Opaque != "" || parsedURL.RawQuery != "" || parsedURL.ForceQuery ||
		parsedURL.Fragment != "" || parsedURL.RawFragment != "" || parsedURL.String() != canonicalURL {
		return "", false, errors.New("code-change canonical URL is invalid")
	}
	typed := ParseTypedComment(body)
	if !HasTypedMarker(body) || !typed.HasHead || len(typed.Errors) > 0 {
		return "", false, errors.New("PROCESS typed comment is invalid")
	}
	if typed.Type != "PROCESS" || typed.ID != processID {
		return "", false, fmt.Errorf("typed comment is %s/%s, expected PROCESS/%s", typed.Type, typed.ID, processID)
	}

	lines := strings.Split(body, "\n")
	linksFound := false
	prIndexes := make([]int, 0, 1)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !linksFound {
			if trimmed == "Links:" {
				linksFound = true
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			break
		}
		name, _, ok := parseLinkLine(trimmed)
		if ok && name == "PR" {
			prIndexes = append(prIndexes, index)
		}
	}
	if !linksFound || len(prIndexes) != 1 {
		return "", false, errors.New("PROCESS typed comment requires exactly one PR link field")
	}
	index := prIndexes[0]
	_, value, ok := parseLinkLine(strings.TrimSpace(lines[index]))
	if !ok {
		return "", false, errors.New("PROCESS PR link field is malformed")
	}
	existing := splitLinkValues(value)
	if len(existing) == 1 && NormalizeURL(existing[0]) == NormalizeURL(canonicalURL) {
		return body, false, nil
	}
	if len(existing) != 0 {
		return "", false, ErrProcessPRLinkConflict
	}
	indent := lines[index][:len(lines[index])-len(strings.TrimLeft(lines[index], " \t"))]
	lines[index] = indent + "- PR: " + canonicalURL
	return strings.Join(lines, "\n"), true, nil
}
