package assignment

import (
	"strings"
	"testing"
)

func TestRequiredOutputPatternHasMeta(t *testing.T) {
	tests := []struct {
		name, pattern string
		want          bool
	}{
		{name: "exact", pattern: "generated/output.go"},
		{name: "component star", pattern: "assets/*.js", want: true},
		{name: "question", pattern: "assets/app?.js", want: true},
		{name: "character class", pattern: "assets/*.[jt]s", want: true},
		{name: "globstar", pattern: "generated/**", want: true},
		{name: "unmatched bracket literal", pattern: "generated/output[.js"},
		{name: "unmatched bracket makes entire path literal", pattern: "generated/*/output[.js"},
		{name: "embedded globstar literal", pattern: "dir/foo**bar"},
		{name: "globstar suffix literal", pattern: "dir/**suffix"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RequiredOutputPatternHasMeta(test.pattern); got != test.want {
				t.Fatalf("RequiredOutputPatternHasMeta(%q) = %t, want %t", test.pattern, got, test.want)
			}
		})
	}
}

func TestMatchRequiredOutputPattern(t *testing.T) {
	tests := []struct {
		name, pattern, path string
		want                bool
	}{
		{name: "exact", pattern: "generated/output.go", path: "generated/output.go", want: true},
		{name: "component star", pattern: "assets/*.js", path: "assets/app.js", want: true},
		{name: "component star does not cross slash", pattern: "assets/*.js", path: "assets/chunks/app.js"},
		{name: "globstar descendants", pattern: "dist/**", path: "dist/assets/app.js", want: true},
		{name: "globstar requires descendant", pattern: "dist/**", path: "dist"},
		{name: "middle globstar may be empty", pattern: "web/**/app.js", path: "web/app.js", want: true},
		{name: "middle globstar spans directories", pattern: "web/**/app.js", path: "web/assets/chunks/app.js", want: true},
		{name: "character class", pattern: "assets/*.[jt]s", path: "assets/app.ts", want: true},
		{name: "question", pattern: "assets/app?.js", path: "assets/app1.js", want: true},
		{name: "unmatched bracket literal", pattern: "generated/output[.js", path: "generated/output[.js", want: true},
		{name: "unmatched bracket prevents glob matching", pattern: "generated/*/output[.js", path: "generated/v1/output[.js"},
		{name: "embedded globstar literal", pattern: "dir/foo**bar", path: "dir/foo**bar", want: true},
		{name: "embedded globstar does not match", pattern: "dir/foo**bar", path: "dir/foobazbar"},
		{name: "globstar suffix literal", pattern: "dir/**suffix", path: "dir/**suffix", want: true},
		{name: "globstar suffix does not match", pattern: "dir/**suffix", path: "dir/valuesuffix"},
		{name: "different extension", pattern: "assets/*.js", path: "assets/app.css"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := MatchRequiredOutputPattern(test.pattern, test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("MatchRequiredOutputPattern(%q, %q) = %t, want %t", test.pattern, test.path, got, test.want)
			}
		})
	}
}

func TestAssignmentAcceptsLiteralRequiredOutputWithUnmatchedBracket(t *testing.T) {
	value := implementationAssignment()
	value.Implementation.Generators[0].RequiredOutputs = []string{"generated/output[.js"}
	if err := value.Validate(); err != nil {
		t.Fatalf("literal required output with unmatched bracket rejected: %v", err)
	}
}

func TestAssignmentAcceptsLiteralRequiredOutputsWithEmbeddedGlobstar(t *testing.T) {
	value := implementationAssignment()
	value.Implementation.Generators[0].RequiredOutputs = []string{"dir/foo**bar", "dir/**suffix"}
	if err := value.Validate(); err != nil {
		t.Fatalf("literal required outputs with embedded globstar rejected: %v", err)
	}
}

func TestAssignmentRejectsUnsafeRequiredOutputPatterns(t *testing.T) {
	for _, pattern := range []string{"/absolute/**", "../outside/**", "dir\\file"} {
		t.Run(pattern, func(t *testing.T) {
			value := implementationAssignment()
			value.Implementation.Generators[0].RequiredOutputs = []string{pattern}
			err := value.Validate()
			if err == nil || !strings.Contains(err.Error(), "required_outputs") {
				t.Fatalf("unsafe required output %q accepted: %v", pattern, err)
			}
		})
	}
}
