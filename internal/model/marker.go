package model

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/preview"
)

var markerRe = regexp.MustCompile(`(?s)<!--\s*issue-spec:([^>]*)-->`)

type Marker struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int    `json:"version"`
}

func RenderMarker(commentType, id string, version int) string {
	if version == 0 {
		version = 1
	}
	return fmt.Sprintf("<!-- issue-spec:type=%s id=%s version=%d -->", strings.ToUpper(strings.TrimSpace(commentType)), strings.TrimSpace(id), version)
}

func FindMarker(body string) (Marker, bool, error) {
	return findMarker(preview.SemanticView(CanonicalView(body)))
}

func findMarker(semanticBody string) (Marker, bool, error) {
	matches := markerRe.FindAllStringSubmatch(semanticBody, -1)
	for _, match := range matches {
		attrs := parseMarkerAttrs(match[1])
		if attrs["type"] == "" && attrs["id"] == "" {
			continue
		}
		version := 1
		if attrs["version"] != "" {
			n, err := strconv.Atoi(attrs["version"])
			if err != nil || n <= 0 {
				return Marker{}, true, fmt.Errorf("invalid marker version %q", attrs["version"])
			}
			version = n
		}
		if attrs["type"] == "" || attrs["id"] == "" {
			return Marker{}, true, fmt.Errorf("typed marker must include type and id")
		}
		return Marker{Type: strings.ToUpper(attrs["type"]), ID: attrs["id"], Version: version}, true, nil
	}
	return Marker{}, false, nil
}

func HasTypedMarker(body string) bool {
	_, ok, err := FindMarker(body)
	return ok && err == nil
}

func parseMarkerAttrs(raw string) map[string]string {
	out := map[string]string{}
	for _, field := range strings.Fields(raw) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return out
}
