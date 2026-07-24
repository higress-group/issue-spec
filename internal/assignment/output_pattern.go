package assignment

import (
	"fmt"
	"path"
	"strings"
)

// RequiredOutputPatternHasMeta reports whether a required generator output
// uses the supported repository-path glob grammar.
func RequiredOutputPatternHasMeta(value string) bool {
	hasMeta := false
	for _, component := range strings.Split(value, "/") {
		if component == "**" {
			hasMeta = true
			continue
		}
		if !strings.ContainsAny(component, "*?[") {
			continue
		}
		if _, err := path.Match(component, "candidate"); err != nil {
			// Git permits '[' in file names. Preserve legacy exact-path
			// handling when bracket syntax does not form a valid glob.
			return false
		}
		hasMeta = true
	}
	return hasMeta
}

// ValidateRequiredOutputPattern validates a repository-relative required
// generator output. A pattern may use path.Match syntax within one path
// component and ** as a complete component spanning directory boundaries.
func ValidateRequiredOutputPattern(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "~") || strings.Contains(value, "\\") ||
		strings.ContainsRune(value, 0) || driveQualifiedRequiredOutput(value) {
		return fmt.Errorf("invalid repository-relative output pattern %q", value)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("unsafe repository-relative output pattern %q", value)
		}
		if component == "**" {
			continue
		}
		if strings.Contains(component, "**") {
			return fmt.Errorf("globstar must be a complete path component in %q", value)
		}
	}
	return nil
}

// MatchRequiredOutputPattern matches one Git-tree file path against a validated
// required output pattern. Globstar matches zero or more complete components;
// a trailing globstar still requires a descendant so dir/** does not match a
// file named dir.
func MatchRequiredOutputPattern(pattern, repositoryPath string) (bool, error) {
	return MatchAnyRequiredOutputPattern(pattern, []string{repositoryPath})
}

// MatchAnyRequiredOutputPattern reports whether at least one Git-tree file
// matches pattern while parsing and validating the pattern only once.
func MatchAnyRequiredOutputPattern(pattern string, repositoryPaths []string) (bool, error) {
	if err := ValidateRequiredOutputPattern(pattern); err != nil {
		return false, err
	}
	hasMeta := RequiredOutputPatternHasMeta(pattern)
	var patternParts []string
	if hasMeta {
		patternParts = strings.Split(pattern, "/")
	}
	for _, repositoryPath := range repositoryPaths {
		if err := validateRequiredOutputPath(repositoryPath); err != nil {
			return false, err
		}
		if !hasMeta {
			if pattern == repositoryPath {
				return true, nil
			}
			continue
		}
		if matchRequiredOutputParts(patternParts, strings.Split(repositoryPath, "/")) {
			return true, nil
		}
	}
	return false, nil
}

func matchRequiredOutputParts(patternParts, pathParts []string) bool {
	type state struct{ pattern, path int }
	memo := map[state]bool{}
	seen := map[state]bool{}
	var match func(int, int) bool
	match = func(patternIndex, pathIndex int) bool {
		key := state{pattern: patternIndex, path: pathIndex}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true
		var matched bool
		switch {
		case patternIndex == len(patternParts):
			matched = pathIndex == len(pathParts)
		case patternParts[patternIndex] == "**":
			if patternIndex == len(patternParts)-1 {
				matched = pathIndex < len(pathParts)
			} else {
				matched = match(patternIndex+1, pathIndex) ||
					pathIndex < len(pathParts) && match(patternIndex, pathIndex+1)
			}
		case pathIndex < len(pathParts):
			componentMatch, _ := path.Match(patternParts[patternIndex], pathParts[pathIndex])
			matched = componentMatch && match(patternIndex+1, pathIndex+1)
		}
		memo[key] = matched
		return matched
	}
	return match(0, 0)
}

func validateRequiredOutputPath(value string) error {
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid repository output path %q", value)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("invalid repository output path %q", value)
		}
	}
	return nil
}

func driveQualifiedRequiredOutput(value string) bool {
	return len(value) >= 2 && value[1] == ':' &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z'))
}
