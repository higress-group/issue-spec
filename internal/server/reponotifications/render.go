package reponotifications

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxIssueBodyBytes = 64 << 10
	truncationMarker  = "\n\n[Issue body truncated at 64 KiB]\n"
)

type IssueCreatedSnapshot struct {
	Version          int       `json:"version"`
	ActorLogin       string    `json:"actor_login"`
	ActorDisplayName string    `json:"actor_display_name"`
	RepositoryOwner  string    `json:"repository_owner"`
	RepositoryName   string    `json:"repository_name"`
	IssueID          uuid.UUID `json:"issue_id"`
	IssueNumber      int64     `json:"issue_number"`
	IssueTitle       string    `json:"issue_title"`
	IssueBody        string    `json:"issue_body"`
	BodyTruncated    bool      `json:"body_truncated"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func NormalizeIssueBody(raw string) (string, bool) {
	normalized := strings.ToValidUTF8(raw, "\uFFFD")
	normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	if len(normalized) <= MaxIssueBodyBytes {
		return normalized, false
	}
	limit := MaxIssueBodyBytes
	for limit > 0 && !utf8.RuneStart(normalized[limit]) {
		limit--
	}
	return normalized[:limit] + truncationMarker, true
}

func (s IssueCreatedSnapshot) Validate() error {
	if s.Version != 1 || s.IssueID == uuid.Nil || s.IssueNumber <= 0 || s.OccurredAt.IsZero() ||
		strings.TrimSpace(s.ActorLogin) == "" || strings.TrimSpace(s.RepositoryOwner) == "" ||
		strings.TrimSpace(s.RepositoryName) == "" || strings.TrimSpace(s.IssueTitle) == "" ||
		len(s.IssueBody) > MaxIssueBodyBytes+len(truncationMarker) || !utf8.ValidString(s.IssueBody) {
		return ErrInvalid
	}
	if s.BodyTruncated != strings.HasSuffix(s.IssueBody, truncationMarker) {
		return ErrInvalid
	}
	return nil
}

func RenderIssueCreated(snapshot IssueCreatedSnapshot, canonicalLink string) (string, string, error) {
	if err := snapshot.Validate(); err != nil || strings.TrimSpace(canonicalLink) == "" {
		return "", "", ErrInvalid
	}
	repository := snapshot.RepositoryOwner + "/" + snapshot.RepositoryName
	title := cleanHeader(snapshot.IssueTitle, 160)
	subject := cleanHeader(fmt.Sprintf("[%s] New issue #%d: %s", repository, snapshot.IssueNumber, title), 240)
	body := fmt.Sprintf("%s (@%s) created an issue in %s.\n\n#%d: %s\n\n%s\n\n%s\n",
		cleanText(snapshot.ActorDisplayName, snapshot.ActorLogin), snapshot.ActorLogin, repository,
		snapshot.IssueNumber, snapshot.IssueTitle, snapshot.IssueBody, canonicalLink)
	return subject, body, nil
}

func cleanHeader(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")), " ")
	if value == "" {
		return "Notification"
	}
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return strings.TrimSpace(value[:cut])
}

func cleanText(preferred, fallback string) string {
	preferred = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(preferred, "\r", " "), "\n", " "))
	if preferred == "" {
		return fallback
	}
	return preferred
}
