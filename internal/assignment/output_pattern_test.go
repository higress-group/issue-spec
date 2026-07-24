package assignment

import (
	"fmt"
	"strings"
	"testing"
)

func TestMatchRequiredOutputPattern(t *testing.T) {
	tests := []struct {
		name, pattern, path string
		want                bool
	}{
		{name: "exact-looking pattern", pattern: "generated/output.go", path: "generated/output.go", want: true},
		{name: "component star", pattern: "assets/*.js", path: "assets/app.js", want: true},
		{name: "component star does not cross slash", pattern: "assets/*.js", path: "assets/chunks/app.js"},
		{name: "globstar descendants", pattern: "dist/**", path: "dist/assets/app.js", want: true},
		{name: "globstar requires descendant", pattern: "dist/**", path: "dist"},
		{name: "middle globstar may be empty", pattern: "web/**/app.js", path: "web/app.js", want: true},
		{name: "middle globstar spans directories", pattern: "web/**/app.js", path: "web/assets/chunks/app.js", want: true},
		{name: "character class", pattern: "assets/*.[jt]s", path: "assets/app.ts", want: true},
		{name: "question", pattern: "assets/app?.js", path: "assets/app1.js", want: true},
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

func TestValidateRequiredOutputPatternRejectsInvalidExplicitGlobs(t *testing.T) {
	for _, pattern := range []string{
		"generated/foo**bar.js",
		"generated/**suffix",
		"generated/output[.js",
		"/absolute/**",
		"../outside/**",
		"generated//*.js",
		"dir\\*.js",
	} {
		t.Run(pattern, func(t *testing.T) {
			if err := ValidateRequiredOutputPattern(pattern); err == nil {
				t.Fatalf("invalid explicit glob %q accepted", pattern)
			}
		})
	}
}

func TestAssignmentRequiredOutputKinds(t *testing.T) {
	tests := []struct {
		name    string
		exact   []string
		globs   []string
		wantErr string
	}{
		{
			name:  "only exact",
			exact: []string{"generated/output[.js", "generated/foo**bar.js", "generated/**suffix", "generated/*.js"},
		},
		{
			name:  "only glob",
			globs: []string{"generated/**", "assets/*.[jt]s"},
		},
		{
			name:    "neither",
			wantErr: "at least one required output or required output glob is required",
		},
		{
			name:    "embedded globstar is invalid only as glob",
			globs:   []string{"generated/foo**bar.js"},
			wantErr: "required_output_globs",
		},
		{
			name:    "malformed bracket is invalid only as glob",
			globs:   []string{"generated/output[.js"},
			wantErr: "required_output_globs",
		},
		{
			name:    "unsafe glob",
			globs:   []string{"../outside/**"},
			wantErr: "required_output_globs",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := implementationAssignment()
			value.Implementation.Generators[0].RequiredOutputs = test.exact
			value.Implementation.Generators[0].RequiredOutputGlobs = test.globs
			err := value.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("assignment rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("assignment error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestAssignmentRequiredOutputDuplicatesAreScopedPerField(t *testing.T) {
	value := implementationAssignment()
	value.Implementation.Generators[0].RequiredOutputs = []string{"generated/output.js"}
	value.Implementation.Generators[0].RequiredOutputGlobs = []string{"generated/output.js"}
	if err := value.Validate(); err != nil {
		t.Fatalf("same value with distinct exact and glob intent rejected: %v", err)
	}

	for name, mutate := range map[string]func(*GeneratorPolicy){
		"exact": func(generator *GeneratorPolicy) {
			generator.RequiredOutputs = []string{"generated/output.js", "generated/output.js"}
			generator.RequiredOutputGlobs = nil
		},
		"glob": func(generator *GeneratorPolicy) {
			generator.RequiredOutputs = nil
			generator.RequiredOutputGlobs = []string{"generated/*.js", "generated/*.js"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := implementationAssignment()
			mutate(&value.Implementation.Generators[0])
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate value") {
				t.Fatalf("duplicate %s output error = %v", name, err)
			}
		})
	}
}

func TestAssignmentRequiredOutputCombinedBound(t *testing.T) {
	value := implementationAssignment()
	value.Implementation.Generators[0].RequiredOutputs = make([]string, maxListItems/2)
	value.Implementation.Generators[0].RequiredOutputGlobs = make([]string, maxListItems-maxListItems/2+1)
	for i := range value.Implementation.Generators[0].RequiredOutputs {
		value.Implementation.Generators[0].RequiredOutputs[i] = fmt.Sprintf("exact/%03d", i)
	}
	for i := range value.Implementation.Generators[0].RequiredOutputGlobs {
		value.Implementation.Generators[0].RequiredOutputGlobs[i] = fmt.Sprintf("glob/%03d/*", i)
	}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds 128 combined required output items") {
		t.Fatalf("combined output bound error = %v", err)
	}
}
