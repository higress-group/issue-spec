package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/durable"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestCommentGenerateExamplesWithoutWorkflowOrBackend(t *testing.T) {
	for _, invalidWorkflow := range []bool{false, true} {
		name := "uninitialized"
		if invalidWorkflow {
			name = "invalid-workflow"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if invalidWorkflow {
				if err := os.MkdirAll("issue-spec", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile("issue-spec/config.yaml", []byte("schema: [invalid"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// A regular file cannot be a profile configuration directory.
			t.Setenv(auth.ConfigDirEnv, writeTempInput(t, "unavailable profile config"))
			for _, commentType := range []string{"SPEC", "TASK"} {
				var out, errOut bytes.Buffer
				app := newApp(strings.NewReader("not input JSON"), &out, &errOut)
				app.profileName = "unavailable-profile"
				app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
					t.Fatal("example must not select a backend")
					return auth.GitHubBackendSelection{}, nil
				}
				if code := app.runComment(t.Context(), []string{"generate", "--type", commentType, "--example"}); code != 0 {
					t.Fatalf("%s example exit=%d stderr=%q", commentType, code, errOut.String())
				}
				if !json.Valid(out.Bytes()) || errOut.Len() != 0 {
					t.Fatalf("%s example stdout=%q stderr=%q", commentType, out.String(), errOut.String())
				}
			}
		})
	}
}

func TestCommentGenerateExampleRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name        string
		commentType string
		id          string
		customID    bool
	}{
		{"spec-default", "SPEC", "SPEC-1001", false},
		{"spec-custom-id", "SPEC", "SPEC-41001", true},
		{"task", "TASK", "TASK-2001", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if err := os.MkdirAll("issue-spec", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join("issue-spec", "config.yaml"), []byte("durable_specs:\n  mode: repository\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"comment", "generate", "--type", tc.commentType, "--example"}
			if tc.customID {
				args = append(args, "--id", tc.id)
			}
			var out, errOut bytes.Buffer
			if code := Execute(args, strings.NewReader(""), &out, &errOut); code != 0 {
				t.Fatalf("example exit=%d stderr=%q", code, errOut.String())
			}
			input := out.String()
			if tc.commentType == "TASK" {
				var task templates.TaskInput
				if err := decodeGeneratorInput(input, &task); err != nil {
					t.Fatal(err)
				}
				planning := task.ExecutionPlanning
				if len(task.Covers) == 0 || len(task.Checklist) == 0 || len(planning.OwnedAreas) == 0 ||
					len(planning.SharedTouchpoints) == 0 || len(planning.Dependencies) == 0 ||
					planning.Coupling == "" || planning.ExecutionMode == "" || planning.Complexity == "" {
					t.Fatalf("incomplete TASK example: %+v", task)
				}
			}
			out.Reset()
			errOut.Reset()
			args = []string{"comment", "generate", "--type", tc.commentType, "--id", tc.id, "--status", "confirmed", "--input-file", "-"}
			if code := Execute(args, strings.NewReader(input), &out, &errOut); code != 0 {
				t.Fatalf("generate example exit=%d stderr=%q", code, errOut.String())
			}
			body := out.String()
			prepared, err := model.EnsureTypedBody(tc.commentType, tc.id, body, model.BodyOptions{Status: "confirmed"})
			if err != nil {
				t.Fatal(err)
			}
			if diags := model.ValidateCanonicalBody(tc.commentType, tc.id, "", prepared); len(diags) != 0 {
				t.Fatalf("example is not canonical: %+v", diags)
			}
			if strings.Contains(body, "TBD") {
				t.Fatalf("example contains unresolved defaults: %s", body)
			}
			if tc.commentType == "SPEC" {
				intent, found, err := model.ParseSpecDurableIntent(tc.id, body, root)
				if err != nil || !found || intent.Kind != durable.IntentOperations || len(intent.Operations) != 1 {
					t.Fatalf("durable example = %+v found=%t err=%v", intent, found, err)
				}
			}
		})
	}
}

func TestCommentGenerateExampleRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing-type", []string{"--example"}, "--type is required"},
		{"unknown-type", []string{"--type", "BOGUS", "--example"}, "supported types: SPEC, TASK"},
		{"unsupported-example", []string{"--type", "PROCESS", "--example"}, "supported types: SPEC, TASK"},
		{"conflicting-file", []string{"--type", "SPEC", "--example", "--input-file", "missing.json"}, "--example cannot be combined with --input-file"},
		{"conflicting-stdin", []string{"--type", "TASK", "--example", "--input-file", "-"}, "--example cannot be combined with --input-file"},
		{"conflicting-empty-input", []string{"--type", "SPEC", "--example", "--input-file="}, "--example cannot be combined with --input-file"},
		{"invalid-spec-id", []string{"--type", "SPEC", "--example", "--id", "TASK-1001"}, "must match SPEC-<n>-OP-<nn>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Execute(append([]string{"comment", "generate"}, tc.args...), strings.NewReader(""), &out, &errOut); code != 2 ||
				out.Len() != 0 || !strings.Contains(errOut.String(), tc.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want %q", code, out.String(), errOut.String(), tc.want)
			}
		})
	}
}

func TestCommentGenerateHelpDiscoversExamples(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute([]string{"comment", "generate", "--help"}, strings.NewReader(""), &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "--example") || !strings.Contains(out.String(), "SPEC-1001") || errOut.Len() != 0 {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}
