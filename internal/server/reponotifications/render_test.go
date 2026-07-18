package reponotifications

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestNormalizeIssueBodyBoundsUTF8AndMarksTruncation(t *testing.T) {
	raw := "first\r\nsecond\r" + strings.Repeat("界", MaxIssueBodyBytes)
	got, truncated := NormalizeIssueBody(raw)
	if !truncated || !utf8.ValidString(got) || !strings.Contains(got, "first\nsecond\n") ||
		!strings.HasSuffix(got, truncationMarker) || len(got) > MaxIssueBodyBytes+len(truncationMarker) {
		t.Fatalf("normalized body bytes=%d truncated=%v valid=%v suffix=%v", len(got), truncated, utf8.ValidString(got), strings.HasSuffix(got, truncationMarker))
	}
	short, truncated := NormalizeIssueBody("unchanged\r\nbody")
	if truncated || short != "unchanged\nbody" {
		t.Fatalf("short normalization = %q truncated=%v", short, truncated)
	}
}

func TestRenderIssueCreatedIsPlainTextAndSanitizesSubject(t *testing.T) {
	snapshot := IssueCreatedSnapshot{Version: 1, ActorLogin: "author", ActorDisplayName: "Author",
		RepositoryOwner: "acme", RepositoryName: "widgets", IssueID: uuid.New(), IssueNumber: 42,
		IssueTitle: "line one\r\nBcc: injected", IssueBody: "raw **Markdown**", OccurredAt: time.Now().UTC()}
	subject, body, err := RenderIssueCreated(snapshot, "https://issues.example.test/acme/widgets/issues/42")
	if err != nil || strings.ContainsAny(subject, "\r\n") || !strings.Contains(subject, "[acme/widgets]") {
		t.Fatalf("subject = %q err=%v", subject, err)
	}
	for _, want := range []string{"Author (@author)", "acme/widgets", "#42", "raw **Markdown**", "https://issues.example.test/acme/widgets/issues/42"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %q", want, body)
		}
	}
}
