package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/requirements"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestRequirementsAcceptanceSetupTargetsAndSecretBoundary(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	homeDir := filepath.Join(root, "home")
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	t.Setenv(auth.ConfigDirEnv, configDir)
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_HOME", codexDir)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	browserLog := filepath.Join(root, "browser.log")
	browser := filepath.Join(root, "isolated-browser")
	if err := os.WriteFile(browser, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+browserLog+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", browser)

	secret := "requirements-acceptance-secret-0123456789"
	state := newRequirementsAcceptanceState("public", []string{"read", "contribute"})
	server := newRequirementsAcceptanceServer(t, secret, state)
	defer server.Close()
	common := []string{"--server", server.URL, "--repo", "owner/repo", "--profile", "acceptance", "--json"}

	var transcript bytes.Buffer
	preview := newApp(strings.NewReader(secret+"\n"), &transcript, &transcript)
	preview.resolveRequirementsToken = noRequirementsToken
	previewArgs := append(append([]string(nil), common...), "--agent", "codex", "--token-stdin")
	if code := preview.runRequirementsSetup(t.Context(), previewArgs); code != 0 {
		t.Fatalf("preview exit=%d transcript=%q", code, transcript.String())
	}
	assertRequirementsOutputDoesNotContain(t, transcript.String(), secret)
	var previewResult requirementsSetupResult
	if err := json.Unmarshal(transcript.Bytes(), &previewResult); err != nil {
		t.Fatal(err)
	}
	if previewResult.Applied || previewResult.SkillPlan.Target != requirements.TargetCodex || previewResult.SkillPlan.Action != requirements.ActionCreate {
		t.Fatalf("unexpected preview: %+v", previewResult)
	}
	if _, _, err := auth.ResolveProfile("acceptance", ""); err == nil {
		t.Fatal("preview wrote the profile")
	}
	if _, err := os.Stat(previewResult.SkillPlan.Path); !os.IsNotExist(err) {
		t.Fatalf("preview wrote the skill: %v", err)
	}

	transcript.Reset()
	storedSecret := ""
	applyCodex := newApp(strings.NewReader(secret+"\n"), &transcript, &transcript)
	applyCodex.resolveRequirementsToken = noRequirementsToken
	applyCodex.storeRequirementsToken = func(_ context.Context, _ auth.Profile, token string, insecure bool) (string, error) {
		if insecure {
			return "", fmt.Errorf("plaintext storage was requested")
		}
		storedSecret = token
		return "keyring", nil
	}
	applyArgs := append(append([]string(nil), common...), "--agent", "codex", "--token-stdin", "--yes")
	if code := applyCodex.runRequirementsSetup(t.Context(), applyArgs); code != 0 {
		t.Fatalf("codex apply exit=%d transcript=%q", code, transcript.String())
	}
	assertRequirementsOutputDoesNotContain(t, transcript.String(), secret)
	if storedSecret != secret {
		t.Fatalf("keyring received %q", storedSecret)
	}

	transcript.Reset()
	applyClaude := newApp(strings.NewReader(""), &transcript, &transcript)
	applyClaude.resolveRequirementsToken = func(context.Context, auth.Profile) (auth.Token, error) {
		return auth.Token{Value: secret, Source: "keyring"}, nil
	}
	applyClaude.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) {
		t.Fatal("second target attempted to store the existing PAT")
		return "", nil
	}
	claudeArgs := append(append([]string(nil), common...), "--agent", "claude", "--yes")
	if code := applyClaude.runRequirementsSetup(t.Context(), claudeArgs); code != 0 {
		t.Fatalf("claude apply exit=%d transcript=%q", code, transcript.String())
	}
	assertRequirementsOutputDoesNotContain(t, transcript.String(), secret)

	for _, target := range []string{
		filepath.Join(codexDir, "skills", requirements.SkillName),
		filepath.Join(claudeDir, "skills", requirements.SkillName),
	} {
		assertRequirementsAcceptanceSkillInstall(t, target)
	}
	configured, err := requirements.LoadActiveContext()
	if err != nil || configured.Agent != requirements.TargetClaude || configured.Repository != "owner/repo" {
		t.Fatalf("active context=%+v err=%v", configured, err)
	}
	assertRequirementsTreeDoesNotContain(t, root, secret)
	if _, err := os.Stat(browserLog); !os.IsNotExist(err) {
		t.Fatalf("requirements setup invoked a browser opener: %v", err)
	}
}

func TestRequirementsAcceptanceExecutableJourney(t *testing.T) {
	clearCommandAuthEnv(t)
	root := t.TempDir()
	t.Setenv(auth.ConfigDirEnv, filepath.Join(root, "config"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	browserLog := filepath.Join(root, "browser.log")
	browser := filepath.Join(root, "isolated-browser")
	if err := os.WriteFile(browser, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+browserLog+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", browser)

	secret := "requirements-journey-secret-0123456789"
	state := newRequirementsAcceptanceState("public", []string{"read", "contribute"})
	server := newRequirementsAcceptanceServer(t, secret, state)
	defer server.Close()

	var output bytes.Buffer
	setup := newApp(strings.NewReader(secret+"\n"), &output, &output)
	setup.resolveRequirementsToken = noRequirementsToken
	storedSecret := ""
	setup.storeRequirementsToken = func(_ context.Context, _ auth.Profile, token string, insecure bool) (string, error) {
		if insecure {
			t.Fatal("requirements setup requested plaintext credential storage")
		}
		storedSecret = token
		return "keyring", nil
	}
	if code := setup.runRequirementsSetup(t.Context(), []string{"--server", server.URL, "--repo", "owner/repo", "--profile", "acceptance",
		"--agent", "codex", "--token-stdin", "--yes", "--json"}); code != 0 {
		t.Fatalf("setup exit=%d output=%q", code, output.String())
	}
	if storedSecret != secret {
		t.Fatalf("fake keyring stored %q", storedSecret)
	}
	assertRequirementsOutputDoesNotContain(t, output.String(), secret)
	assertRequirementsAcceptanceSkillInstall(t, filepath.Join(root, "codex", "skills", requirements.SkillName))
	tokenFile := filepath.Join(root, "journey.token")
	if err := os.WriteFile(tokenFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(auth.IssueSpecTokenFileEnv, tokenFile)

	state.resetMutations()
	preview := runRequirementsAcceptanceJourney(t, root, "acceptance", "Add a smaller requirements workflow", false)
	if len(preview.Drafts) != 5 || len(preview.Plan) != 6 || preview.Denial != "" || preview.Handoff != "" {
		t.Fatalf("unconfirmed journey=%+v", preview)
	}
	if mutations := state.mutationKinds(); len(mutations) != 0 {
		t.Fatalf("unconfirmed journey mutated server: %v", mutations)
	}

	state.resetMutations()
	confirmed := runRequirementsAcceptanceJourney(t, root, "acceptance", "Add a smaller requirements workflow", true)
	if !slices.Equal(confirmed.Plan, state.mutationKinds()) {
		t.Fatalf("confirmed plan=%v mutations=%v", confirmed.Plan, state.mutationKinds())
	}
	if len(confirmed.URLs) != len(confirmed.Plan) {
		t.Fatalf("returned URLs=%v plan=%v", confirmed.URLs, confirmed.Plan)
	}
	for _, browserURL := range confirmed.URLs {
		if !strings.HasPrefix(browserURL, server.URL+"/owner/repo/") {
			t.Errorf("non-browser journey URL %q", browserURL)
		}
	}
	if _, err := os.Stat(browserLog); !os.IsNotExist(err) {
		t.Fatalf("journey invoked browser opener instead of returning URLs: %v", err)
	}

	for _, policy := range []string{"members", "disabled"} {
		state.setAccess(policy, []string{"read"})
		state.resetMutations()
		blocked := runRequirementsAcceptanceJourney(t, root, "acceptance", "Keep this as a local draft", true)
		if len(blocked.Drafts) != 5 || blocked.Denial != "allowed_actions does not include contribute" || len(blocked.URLs) != 0 {
			t.Fatalf("policy=%s blocked journey=%+v", policy, blocked)
		}
		if mutations := state.mutationKinds(); len(mutations) != 0 {
			t.Fatalf("policy=%s mutated server: %v", policy, mutations)
		}
	}

	state.setAccess("public", []string{"read", "contribute"})
	state.resetMutations()
	handoff := runRequirementsAcceptanceJourney(t, root, "acceptance", "Design and implement the engineering solution", true)
	if handoff.Handoff != "handoff to the design/engineering workflow" || len(handoff.Drafts) != 0 || len(handoff.URLs) != 0 {
		t.Fatalf("design boundary=%+v", handoff)
	}
	if mutations := state.mutationKinds(); len(mutations) != 0 {
		t.Fatalf("design boundary created design/TASK/PROCESS/code mutations: %v", mutations)
	}
	if err := os.Remove(tokenFile); err != nil {
		t.Fatal(err)
	}
	assertRequirementsTreeDoesNotContain(t, root, secret)
}

type requirementsAcceptanceJourneyResult struct {
	Drafts  map[string]string
	Plan    []string
	URLs    []string
	Denial  string
	Handoff string
}

var requirementsAcceptancePlan = []string{
	"issue.simple", "issue.proposal", "comment.SPEC", "comment.QUESTION", "issue.author-update", "comment.discussion",
}

func runRequirementsAcceptanceJourney(t *testing.T, root, profile, request string, confirmed bool) requirementsAcceptanceJourneyResult {
	t.Helper()
	result := requirementsAcceptanceJourneyResult{}
	lowerRequest := strings.ToLower(request)
	if strings.Contains(lowerRequest, "design") || strings.Contains(lowerRequest, "engineering") {
		result.Handoff = "handoff to the design/engineering workflow"
		return result
	}

	statusOutput := runRequirementsAcceptanceCLI(t, profile, nil, "requirements", "status", "--json")
	var status requirementsStatusResult
	if err := json.Unmarshal(statusOutput, &status); err != nil {
		t.Fatal(err)
	}
	_, proposalDraft, _ := templates.ProposalIssue("requirements-acceptance")
	specInput := filepath.Join(root, "spec-input.json")
	if err := os.WriteFile(specInput, []byte(specInputJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	specDraft := runRequirementsAcceptanceCLI(t, profile, nil, "comment", "generate", "--type", "SPEC", "--id", "SPEC-001",
		"--status", "confirmed", "--scope", "requirements acceptance", "--input-file", specInput)
	result.Drafts = map[string]string{
		"simple": request, "proposal": proposalDraft, "spec": string(specDraft),
		"question": "Should the compact flow remain the default?", "discussion": "This keeps ordinary discussion on the issue.",
	}
	result.Plan = append([]string(nil), requirementsAcceptancePlan...)
	if !status.CanContribute {
		result.Denial = "allowed_actions does not include contribute"
		return result
	}
	if !confirmed {
		return result
	}

	simpleBody := filepath.Join(root, "simple.md")
	updatedBody := filepath.Join(root, "simple-updated.md")
	specBody := filepath.Join(root, "spec.md")
	discussionBody := filepath.Join(root, "discussion.md")
	for path, body := range map[string]string{
		simpleBody: request + "\n", updatedBody: request + "\n\nAuthor clarification.\n", specBody: string(specDraft),
		discussionBody: result.Drafts["discussion"] + "\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	simple := decodeRequirementsAcceptanceResult(t, runRequirementsAcceptanceCLI(t, profile, nil, "issue", "create", "simple",
		"--repo", "owner/repo", "--title", "Smaller requirements workflow", "--body-file", simpleBody, "--json"))
	proposal := decodeRequirementsAcceptanceResult(t, runRequirementsAcceptanceCLI(t, profile, nil, "issue", "create", "proposal",
		"--repo", "owner/repo", "--change", "requirements-acceptance", "--json"))
	proposalNumber := int(proposal["number"].(float64))
	simpleNumber := int(simple["number"].(float64))
	spec := decodeRequirementsAcceptanceResult(t, runRequirementsAcceptanceCLI(t, profile, nil, "comment", "upsert",
		"--repo", "owner/repo", "--issue", strconv.Itoa(proposalNumber), "--type", "SPEC", "--id", "SPEC-001",
		"--body-file", specBody, "--status", "confirmed", "--scope", "requirements acceptance", "--agent", "Worker-P002", "--json"))
	question := decodeRequirementsAcceptanceResult(t, runRequirementsAcceptanceCLI(t, profile, nil, "question", "create",
		"--repo", "owner/repo", "--issue", strconv.Itoa(proposalNumber), "--id", "QUESTION-001",
		"--question", result.Drafts["question"], "--scope", "requirements acceptance", "--json"))
	update := decodeRequirementsAcceptanceResult(t, runRequirementsAcceptanceCLI(t, profile, nil, "issue", "update",
		"--repo", "owner/repo", "--issue", strconv.Itoa(simpleNumber), "--title", "Smaller requirements workflow clarified",
		"--body-file", updatedBody, "--json"))
	discussion := decodeRequirementsAcceptanceResult(t, runRequirementsAcceptanceCLI(t, profile, nil, "comment", "create",
		"--repo", "owner/repo", "--issue", strconv.Itoa(simpleNumber), "--body-file", discussionBody, "--json"))
	for _, output := range []map[string]any{simple, proposal, spec, question, update, discussion} {
		browserURL, ok := output["url"].(string)
		if !ok || browserURL == "" {
			t.Fatalf("CLI result did not return browser URL: %#v", output)
		}
		result.URLs = append(result.URLs, browserURL)
	}
	return result
}

func runRequirementsAcceptanceCLI(t *testing.T, profile string, input []byte, args ...string) []byte {
	t.Helper()
	fullArgs := append([]string{"--profile", profile}, args...)
	var output, errOutput bytes.Buffer
	if code := Execute(fullArgs, bytes.NewReader(input), &output, &errOutput); code != 0 {
		t.Fatalf("issue-spec %s exit=%d stdout=%q stderr=%q", strings.Join(args, " "), code, output.String(), errOutput.String())
	}
	return append([]byte(nil), output.Bytes()...)
}

func decodeRequirementsAcceptanceResult(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode CLI result %q: %v", raw, err)
	}
	return result
}

type requirementsAcceptanceState struct {
	mu            sync.Mutex
	policy        string
	actions       []string
	nextIssue     int
	nextComment   int64
	issues        map[int]github.Issue
	comments      map[int][]github.Comment
	mutationLog   []string
	serverBaseURL string
}

func newRequirementsAcceptanceState(policy string, actions []string) *requirementsAcceptanceState {
	return &requirementsAcceptanceState{policy: policy, actions: append([]string(nil), actions...), nextIssue: 100, nextComment: 1000,
		issues: map[int]github.Issue{}, comments: map[int][]github.Comment{}}
}

func (s *requirementsAcceptanceState) setAccess(policy string, actions []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = policy
	s.actions = append([]string(nil), actions...)
}

func (s *requirementsAcceptanceState) access() (string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy, append([]string(nil), s.actions...)
}

func (s *requirementsAcceptanceState) canContribute() bool {
	_, actions := s.access()
	return slices.Contains(actions, "contribute")
}

func (s *requirementsAcceptanceState) resetMutations() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mutationLog = nil
}

func (s *requirementsAcceptanceState) mutationKinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.mutationLog...)
}

func (s *requirementsAcceptanceState) recordMutation(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mutationLog = append(s.mutationLog, kind)
}

func newRequirementsAcceptanceServer(t *testing.T, secret string, state *requirementsAcceptanceState) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/meta" {
			if r.Header.Get("Authorization") != "" {
				t.Error("metadata discovery included authorization")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"api_version": "v1", "server_instance_id": "issue-spec:acceptance", "api_url": server.URL,
				"native_api_url": server.URL + "/api/v1", "web_url": server.URL,
				"transport": map[string]any{"mode": "loopback-http", "secure": false},
				"features":  map[string]any{"requirements_onboarding": true, "search": true},
			})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		policy, actions := state.access()
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "read:user, issues:write")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "external-user"})
		case "/api/v1/context":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":          map[string]any{"id": "user-id", "login": "external-user"},
				"credential":    map[string]any{"kind": "pat", "scopes": []string{"read:user", "issues:write"}, "repository_restricted": false},
				"organizations": []map[string]any{{"id": "org-id", "name": "owner"}},
			})
		case "/api/v1/context/orgs/org-id/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{
				"repository":           map[string]any{"id": "repo-id", "organization_id": "org-id", "name": "repo", "visibility": "public", "contribution_policy": policy},
				"effective_permission": "read", "allowed_actions": actions,
			}}})
		default:
			handleRequirementsAcceptanceREST(t, state, w, r)
		}
	}))
	state.serverBaseURL = server.URL
	return server
}

func handleRequirementsAcceptanceREST(t *testing.T, state *requirementsAcceptanceState, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	const prefix = "/repos/owner/repo"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if r.Method == http.MethodPost && rest == "/issues" {
		if !state.canContribute() {
			http.Error(w, `{"message":"contribute denied"}`, http.StatusForbidden)
			return
		}
		var input struct {
			Title  string   `json:"title"`
			Body   string   `json:"body"`
			Labels []string `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		kind := "issue.simple"
		if slices.Equal(input.Labels, []string{"issue-spec/proposal"}) && strings.Contains(input.Body, "issue-spec:issue=proposal") {
			kind = "issue.proposal"
		} else if len(input.Labels) != 0 || strings.Contains(input.Body, "issue-spec:issue=") {
			t.Errorf("simple issue unexpectedly carried typed metadata: labels=%v body=%q", input.Labels, input.Body)
		}
		state.mu.Lock()
		state.nextIssue++
		number := state.nextIssue
		issue := github.Issue{Number: number, Title: input.Title, Body: input.Body, State: "open",
			HTMLURL: state.serverBaseURL + "/owner/repo/issues/" + strconv.Itoa(number), URL: state.serverBaseURL + r.URL.Path + "/" + strconv.Itoa(number)}
		state.issues[number] = issue
		state.mu.Unlock()
		state.recordMutation(kind)
		_ = json.NewEncoder(w).Encode(issue)
		return
	}
	if !strings.HasPrefix(rest, "/issues/") {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/issues/"), "/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	issueNumber, err := strconv.Atoi(parts[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		state.mu.Lock()
		issue, ok := state.issues[issueNumber]
		state.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(issue)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPatch {
		if !state.canContribute() {
			http.Error(w, `{"message":"contribute denied"}`, http.StatusForbidden)
			return
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 2 || input["title"] == nil || input["body"] == nil {
			t.Errorf("author update escaped title/body boundary: %#v", input)
		}
		state.mu.Lock()
		issue := state.issues[issueNumber]
		issue.Title, _ = input["title"].(string)
		issue.Body, _ = input["body"].(string)
		state.issues[issueNumber] = issue
		state.mu.Unlock()
		state.recordMutation("issue.author-update")
		_ = json.NewEncoder(w).Encode(issue)
		return
	}
	if len(parts) == 2 && parts[1] == "comments" && r.Method == http.MethodGet {
		state.mu.Lock()
		comments := append([]github.Comment(nil), state.comments[issueNumber]...)
		state.mu.Unlock()
		_ = json.NewEncoder(w).Encode(comments)
		return
	}
	if len(parts) == 2 && parts[1] == "comments" && r.Method == http.MethodPost {
		if !state.canContribute() {
			http.Error(w, `{"message":"contribute denied"}`, http.StatusForbidden)
			return
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		body := input["body"]
		typed := model.ParseTypedComment(body)
		kind := "comment.discussion"
		if typed.Type != "" {
			kind = "comment." + typed.Type
			if typed.Type == "SPEC" {
				if diagnostics := model.ValidateCanonicalBody("SPEC", typed.ID, "", body); len(diagnostics) != 0 {
					t.Errorf("journey wrote noncanonical SPEC: %+v", diagnostics)
				}
			}
		} else if model.IsLikelyTyped(body) {
			t.Errorf("ordinary discussion looked typed: %q", body)
		}
		state.mu.Lock()
		state.nextComment++
		commentID := state.nextComment
		comment := github.Comment{ID: commentID, Body: body,
			HTMLURL: state.serverBaseURL + "/owner/repo/issues/" + strconv.Itoa(issueNumber) + "#issuecomment-" + strconv.FormatInt(commentID, 10),
			URL:     state.serverBaseURL + "/repos/owner/repo/issues/comments/" + strconv.FormatInt(commentID, 10)}
		state.comments[issueNumber] = append(state.comments[issueNumber], comment)
		state.mu.Unlock()
		state.recordMutation(kind)
		_ = json.NewEncoder(w).Encode(comment)
		return
	}
	http.NotFound(w, r)
}

func assertRequirementsAcceptanceSkillInstall(t *testing.T, target string) {
	t.Helper()
	if info, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("skill target %s: info=%v err=%v", target, info, err)
	}
	raw, err := os.ReadFile(filepath.Join(target, requirements.ManagedManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest requirements.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	wantContentID, err := requirements.ContentID()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != requirements.ManifestSchema || manifest.Name != requirements.SkillName || manifest.ContentID != wantContentID {
		t.Fatalf("installed manifest=%+v want_content_id=%s", manifest, wantContentID)
	}
}

func assertRequirementsTreeDoesNotContain(t *testing.T, root, secret string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == "isolated-browser" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte(secret)) {
			return fmt.Errorf("secret persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertRequirementsOutputDoesNotContain(t *testing.T, output, secret string) {
	t.Helper()
	if strings.Contains(output, secret) {
		t.Fatal("requirements terminal output exposed the PAT")
	}
}
