package repository

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func ValidateCloneURL(raw string) (string, error) {
	return validateRepositoryURL(raw, true)
}

func ValidateWebURL(raw string) (string, error) {
	return validateRepositoryURL(raw, false)
}

func validateRepositoryURL(raw string, clone bool) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" ||
		!strings.HasPrefix(parsed.Path, "/") || strings.ContainsAny(parsed.Host, "\\\r\n\t ") ||
		strings.ContainsAny(parsed.Path, "\\\r\n\x00") {
		return "", fmt.Errorf("repository URL is not a safe hierarchical coordinate")
	}
	if clone {
		if parsed.Scheme != "https" && parsed.Scheme != "ssh" {
			return "", fmt.Errorf("clone URL scheme must be https or ssh")
		}
	} else if parsed.Scheme != "https" {
		return "", fmt.Errorf("web URL scheme must be https")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("repository URL host is required")
	}
	if port := parsed.Port(); port != "" {
		numeric, err := strconv.Atoi(port)
		if err != nil || numeric < 1 || numeric > 65535 {
			return "", fmt.Errorf("repository URL port is invalid")
		}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}
