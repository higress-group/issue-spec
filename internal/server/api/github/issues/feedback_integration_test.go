package issues_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commands"
	"github.com/higress-group/issue-spec/internal/feedback"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestFeedbackHiRoute495SelfHosted(t *testing.T) {
	env := newEnvironment(t, models.VisibilityPublic)
	if _, err := env.pool.Exec(t.Context(), "UPDATE repos SET contribution_policy = 'public' WHERE id = $1", env.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(t.Context(), `INSERT INTO source_bindings
		(id, organization_id, repository_id, provider_key, external_repository_id, clone_url, web_url,
		 default_branch, version, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, 'code.example', 'release-catalog', 'https://code.example/acme/catalog.git',
		 'https://code.example/acme/catalog', 'main', 1, $4, $4)`,
		uuid.New(), env.scope.OrgID, env.scope.RepoID, env.owner.User.ID); err != nil {
		t.Fatal(err)
	}
	contributor := env.addOutsider(t, "contributor")
	env.addOutsider(t, "unconfirmed-reporter")
	mux := env.mux(t)
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		principal, err := env.bearer.AuthenticateBearer(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(github.User{ID: codec.StableNumericID(principal.User.ID.String()), Login: principal.User.Login})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	const repo = "acme/widgets"
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv(auth.IssueSpecTokenFileEnv, "")
	t.Setenv("ISSUE_SPEC_TOKEN", "")
	for _, actor := range []string{"owner", "contributor", "unconfirmed-reporter"} {
		profile := auth.Profile{Name: actor, Kind: auth.ProfileKindHosted, APIURL: server.URL,
			NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL, ServerInstanceID: "feedback-495-test"}
		if err := auth.SaveProfile(profile, false); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.StoreProfileToken(t.Context(), profile, actor, true); err != nil {
			t.Fatal(err)
		}
	}

	ctx := t.Context()
	owner, err := github.NewClientWithOptions(github.ClientOptions{BaseURL: server.URL, Token: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.CreateLabel(ctx, repo, "existing-category", "0052cc", "Category kept during escalation"); err != nil {
		t.Fatal(err)
	}
	newIssue := func(title, body string, labels ...string) github.Issue {
		t.Helper()
		issue, err := owner.CreateIssue(ctx, repo, title, body, labels)
		if err != nil {
			t.Fatal(err)
		}
		return issue
	}
	execute := func(profile, input string, args ...string) (int, string) {
		fullArgs := append([]string{"--profile", profile, "feedback"}, args...)
		var stdout, stderr bytes.Buffer
		code := commands.Execute(fullArgs, strings.NewReader(input), &stdout, &stderr)
		if code != 0 {
			return code, stderr.String()
		}
		return code, stdout.String()
	}

	policy := feedback.Policy{
		Threshold: 3, PriorityLabel: "priority/high",
		TrustedReporterIDs: []string{
			strconv.FormatInt(codec.StableNumericID(env.owner.User.ID.String()), 10),
			strconv.FormatInt(codec.StableNumericID(contributor.User.ID.String()), 10),
		},
	}
	policyIssue := newIssue("Feedback policy", jsonBody(policy))
	policyNumber := strconv.Itoa(policyIssue.Number)
	configure := func(profile string, flags ...string) (int, string) {
		args := append([]string{"configure", "--repo", repo, "--issue", policyNumber, "--json"}, flags...)
		return execute(profile, "", args...)
	}
	if code, out := configure("owner", "--dry-run"); code != 0 || !strings.Contains(out, `"dry_run": true`) {
		t.Fatalf("policy preview exit=%d output=%s", code, out)
	}
	if _, err := owner.GetRepositoryLabel(ctx, repo, "issue-spec/feedback-policy"); !errors.Is(err, github.ErrRepositoryLabelNotFound) {
		t.Fatalf("policy preview mutated label definition: %v", err)
	}
	if code, out := configure("owner"); code != 0 {
		t.Fatalf("publish policy exit=%d output=%s", code, out)
	}

	events := make([]string, 0, 3)
	for _, name := range []string{"release-0.6.2", "release-0.6.3", "synthetic-third-release"} {
		data, err := os.ReadFile(filepath.Join("../../../../../testdata/workflow-feedback", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, string(data))
	}
	record := func(profile string, issue github.Issue, event int, flags ...string) (feedback.Result, error) {
		args := append([]string{"record", "--repo", repo, "--issue", strconv.Itoa(issue.Number), "--event-file", "-", "--json"}, flags...)
		code, out := execute(profile, events[event], args...)
		if code != 0 {
			return feedback.Result{}, errors.New(out)
		}
		var result feedback.Result
		err := json.Unmarshal([]byte(out), &result)
		return result, err
	}
	check := func(profile string, issue github.Issue, event, count int, promotion string, flags ...string) feedback.Result {
		t.Helper()
		result, err := record(profile, issue, event, flags...)
		if err != nil || result.ConfirmedCount != count || result.Promotion.Status != promotion {
			t.Fatalf("%s Issue #%d event=%d result=%+v err=%v", profile, issue.Number, event, result, err)
		}
		return result
	}
	seed := func(issue github.Issue) {
		t.Helper()
		check("owner", issue, 0, 1, "below_threshold")
		check("owner", issue, 1, 2, "below_threshold")
	}
	suggest := newIssue("HiRoute #495 suggestion case", "Two independent catalog merges", "existing-category")
	seed(suggest)
	preview := check("owner", suggest, 1, 2, "below_threshold", "--dry-run")
	if !preview.DryRun || preview.EventStatus != "already_recorded" {
		t.Fatalf("unexpected preview %+v", preview)
	}
	replayed := make(chan error, 2)
	for range 2 {
		go func() {
			result, err := record("owner", suggest, 1)
			if err == nil && (result.EventStatus != "already_recorded" || result.ConfirmedCount != 2) {
				err = errors.New("replay changed the recorded occurrence count")
			}
			replayed <- err
		}()
	}
	for range 2 {
		if err := <-replayed; err != nil {
			t.Fatal(err)
		}
	}
	check("unconfirmed-reporter", suggest, 2, 2, "below_threshold")
	check("contributor", suggest, 2, 3, "suggested")

	policy.Mode = "auto"
	policyBody := jsonBody(policy)
	if _, err := owner.UpdateIssue(ctx, repo, policyIssue.Number, github.UpdateIssueOptions{Body: &policyBody}); err != nil {
		t.Fatal(err)
	}
	if _, err := record("owner", suggest, 2); err == nil || !strings.Contains(err.Error(), "differs from the published revision") {
		t.Fatalf("an unpublished policy body became active: %v", err)
	}
	if code, out := configure("contributor"); code == 0 {
		t.Fatalf("contributor altered policy label definition: %s", out)
	}
	if code, out := configure("owner"); code != 0 {
		t.Fatalf("publish automatic policy exit=%d output=%s", code, out)
	}

	if missing := check("owner", suggest, 2, 3, "pending"); !strings.Contains(missing.Promotion.Reason, "label unavailable") {
		t.Fatalf("missing label did not leave promotion pending: %+v", missing)
	}
	if _, err := owner.CreateLabel(ctx, repo, "priority/high", "d73a4a", "High priority"); err != nil {
		t.Fatal(err)
	}
	auto := newIssue("HiRoute #495 automatic case", "Independent catalog merges with automatic triage", "existing-category")
	seed(auto)
	partial := check("contributor", auto, 2, 3, "pending")
	if partial.EventStatus != "recorded" || partial.CommentID <= 0 || !strings.Contains(partial.Promotion.Reason, "HTTP 403") {
		t.Fatalf("permission failure lost evidence: %+v", partial)
	}
	final := check("owner", auto, 2, 3, "applied")
	if final.CommentID != partial.CommentID || final.EventStatus != "already_recorded" {
		t.Fatalf("label retry changed occurrence identity: %+v", final)
	}
	comments, err := owner.ListIssueComments(ctx, repo, auto.Number)
	if err != nil || len(comments) != 3 {
		t.Fatalf("comments after repair=%d err=%v", len(comments), err)
	}
	current, err := owner.GetIssue(ctx, repo, auto.Number)
	if err != nil || !slices.ContainsFunc(current.Labels, func(label github.Label) bool { return label.Name == "existing-category" }) {
		t.Fatalf("initial category lost: Issue=%+v err=%v", current, err)
	}
	check("owner", auto, 2, 3, "already_at_priority")

	if _, err := owner.CreateLabel(ctx, repo, "issue-spec/feedback-hold", "bfdadc", "Maintainer hold"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.AddIssueLabels(ctx, repo, suggest.Number, []string{"issue-spec/feedback-hold"}); err != nil {
		t.Fatal(err)
	}
	check("owner", suggest, 2, 3, "held")
	closed := "closed"
	if _, err := owner.UpdateIssue(ctx, repo, suggest.Number, github.UpdateIssueOptions{State: &closed}); err != nil {
		t.Fatal(err)
	}
	check("owner", suggest, 2, 3, "closed")
}
