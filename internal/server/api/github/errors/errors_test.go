package errors_test

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	client "github.com/higress-group/issue-spec/internal/github"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
)

func TestMissingAndInvisibleNotFoundAreByteEquivalent(t *testing.T) {
	write := func() *httptest.ResponseRecorder {
		result := httptest.NewRecorder()
		apierrors.WriteGitHub(result, apierrors.NotFound("same-request"))
		return result
	}
	missing := write()
	invisible := write()
	if missing.Code != http.StatusNotFound || !bytes.Equal(missing.Body.Bytes(), invisible.Body.Bytes()) {
		t.Fatalf("responses differ:\n%s\n%s", missing.Body.Bytes(), invisible.Body.Bytes())
	}
	want := `{"message":"Not Found","documentation_url":"https://docs.github.com/rest"}` + "\n"
	if missing.Body.String() != want {
		t.Fatalf("golden mismatch:\n got %q\nwant %q", missing.Body.String(), want)
	}
}

func TestDuplicateLabelEnvelopeMatchesCurrentClientIdempotency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apierrors.WriteGitHub(w, apierrors.LabelAlreadyExists("request-1"))
	}))
	defer server.Close()
	githubClient := client.NewClientWithBaseURL("issues.test", server.URL, "", server.Client())
	result, err := githubClient.CreateLabel(context.Background(), "o/r", "bug", "ff0000", "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || result.Created || result.Name != "bug" {
		t.Fatalf("result = %+v", result)
	}
}

func TestGitHubErrorStatusHeadersAndShapes(t *testing.T) {
	cases := []struct {
		name string
		err  apierrors.GitHubError
		code int
	}{
		{"unauthorized", apierrors.Unauthorized("r1"), 401},
		{"forbidden", apierrors.Forbidden("r1"), 403},
		{"not_found", apierrors.NotFound("r1"), 404},
		{"already_exists", apierrors.LabelAlreadyExists("r1"), 422},
		{"rate", apierrors.TooManyRequests("r1", 1500*time.Millisecond), 429},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			res := httptest.NewRecorder()
			apierrors.WriteGitHub(res, item.err)
			if res.Code != item.code || res.Header().Get("X-Request-ID") != "r1" {
				t.Fatalf("response = %d %+v", res.Code, res.Header())
			}
			var envelope apierrors.Envelope
			if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil || envelope.Message == "" {
				t.Fatalf("body = %q err=%v", res.Body.String(), err)
			}
		})
	}
	if got := apierrors.RetryAfterSeconds(cases[len(cases)-1].err); got != 2 {
		t.Fatalf("Retry-After = %d", got)
	}
}

func TestNativeProblemCapabilityAndRedaction(t *testing.T) {
	err := apierrors.RequireCapability("pull create", "pull_requests", false)
	var unsupported *apierrors.UnsupportedCapabilityError
	if !stderrors.As(err, &unsupported) {
		t.Fatalf("error = %T %v", err, err)
	}
	problem := apierrors.UnsupportedCapability("pull create", "pull_requests", "req-7")
	problem.Detail += " tenant=org-secret Bearer tok_123 token=tok_123"
	problem.Instance = "/api/v1/orgs/org-secret"
	redactor := apierrors.NewRedactor([]string{"tok_123"}, []string{"org-secret"})
	problem = redactor.Problem(problem)
	res := httptest.NewRecorder()
	apierrors.WriteProblem(res, problem)
	if res.Code != http.StatusNotImplemented || res.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response = %d %+v", res.Code, res.Header())
	}
	body := res.Body.String()
	for _, forbidden := range []string{"tok_123", "org-secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("secret %q leaked in %s", forbidden, body)
		}
	}
	for _, want := range []string{`"code":"unsupported_capability"`, `"request_id":"req-7"`, `"capability":"pull_requests"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}
