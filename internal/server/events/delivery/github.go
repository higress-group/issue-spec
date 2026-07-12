package delivery

import (
	"encoding/json"
	"strings"

	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
)

type notificationPolicy struct {
	IssueActions, CommentActions, IssueKinds, CommentClasses, ActorClasses []string
}

func matchesNotification(envelope outbox.Envelope, policy notificationPolicy) (bool, string) {
	facts := envelope.Notification
	if facts == nil {
		return false, "notification_facts_missing"
	}
	if !contains(policy.ActorClasses, facts.ActorClass) {
		return false, "actor_class_filtered"
	}
	if !contains(policy.IssueKinds, facts.IssueKind) {
		return false, "issue_kind_filtered"
	}
	if envelope.Comment == nil {
		if !contains(policy.IssueActions, notificationAction(envelope)) {
			return false, "issue_action_filtered"
		}
		return true, ""
	}
	if !contains(policy.CommentActions, envelope.Action) {
		return false, "comment_action_filtered"
	}
	if !contains(policy.CommentClasses, facts.CommentClass) {
		return false, "comment_class_filtered"
	}
	return true, ""
}

func notificationAction(envelope outbox.Envelope) string {
	if envelope.EventType == "issue.created" {
		return "opened"
	}
	return envelope.Action
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func renderGitHub(envelope outbox.Envelope, apiOrigin, webOrigin string) ([]byte, string, error) {
	facts := envelope.Notification
	apiOrigin = strings.TrimRight(apiOrigin, "/")
	webOrigin = strings.TrimRight(webOrigin, "/")
	if apiOrigin == "" {
		apiOrigin = "http://127.0.0.1:8080"
	}
	if webOrigin == "" {
		webOrigin = apiOrigin
	}
	repoPath := "/repos/" + facts.Repository.FullName
	issuePath := repoPath + "/issues/" + itoa(envelope.Issue.Number)
	user := func(value outbox.NotificationUser) map[string]any {
		return map[string]any{"login": value.Login, "id": codec.StableNumericID(value.ID.String()),
			"node_id": value.ID.String(), "type": "User", "site_admin": false,
			"url": apiOrigin + "/users/" + value.Login, "html_url": webOrigin + "/users/" + value.Login}
	}
	labels := make([]map[string]any, 0, len(facts.Issue.Labels))
	for _, label := range facts.Issue.Labels {
		labels = append(labels, map[string]any{"name": label.Name, "color": label.Color, "description": label.Description})
	}
	issue := map[string]any{"id": codec.StableNumericID(facts.Issue.ID.String()), "node_id": facts.Issue.ID.String(),
		"url": apiOrigin + issuePath, "repository_url": apiOrigin + repoPath,
		"labels_url": apiOrigin + issuePath + "/labels{/name}", "comments_url": apiOrigin + issuePath + "/comments",
		"events_url": apiOrigin + issuePath + "/events", "html_url": webOrigin + "/" + facts.Repository.FullName + "/issues/" + itoa(envelope.Issue.Number),
		"number": facts.Issue.Number, "state": facts.Issue.State, "title": facts.Issue.Title,
		"body": facts.Issue.Body, "user": user(facts.Issue.Author), "labels": labels,
		"created_at": facts.Issue.CreatedAt, "updated_at": facts.Issue.UpdatedAt, "closed_at": facts.Issue.ClosedAt}
	owner := user(outbox.NotificationUser{ID: facts.Organization.ID, Login: facts.Organization.Login})
	owner["type"] = "Organization"
	repository := map[string]any{"id": codec.StableNumericID(facts.Repository.ID.String()), "node_id": facts.Repository.ID.String(),
		"name": facts.Repository.Name, "full_name": facts.Repository.FullName, "private": facts.Repository.Private, "owner": owner,
		"html_url": webOrigin + "/" + facts.Repository.FullName, "url": apiOrigin + repoPath,
		"issues_url": apiOrigin + repoPath + "/issues{/number}"}
	organization := map[string]any{"login": facts.Organization.Login,
		"id": codec.StableNumericID(facts.Organization.ID.String()), "node_id": facts.Organization.ID.String(),
		"url":        apiOrigin + "/orgs/" + facts.Organization.Login,
		"repos_url":  apiOrigin + "/orgs/" + facts.Organization.Login + "/repos",
		"avatar_url": webOrigin + "/api/v1/avatar/org/" + facts.Organization.ID.String()}
	payload := map[string]any{"action": notificationAction(envelope), "issue": issue, "repository": repository,
		"organization": organization, "sender": user(facts.Sender)}
	eventName := "issues"
	if facts.Comment != nil {
		eventName = "issue_comment"
		payload["comment"] = map[string]any{"id": facts.Comment.NumericID, "node_id": facts.Comment.ID.String(),
			"url":       apiOrigin + repoPath + "/issues/comments/" + itoa(facts.Comment.NumericID),
			"html_url":  webOrigin + "/" + facts.Repository.FullName + "/issues/" + itoa(envelope.Issue.Number) + "#issuecomment-" + itoa(facts.Comment.NumericID),
			"issue_url": apiOrigin + issuePath, "body": facts.Comment.Body, "user": user(facts.Comment.Author),
			"created_at": facts.Comment.CreatedAt, "updated_at": facts.Comment.UpdatedAt}
	}
	body, err := json.Marshal(payload)
	return body, eventName, err
}

func itoa(value int64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = digits[value%10]
		value /= 10
	}
	return string(buffer[index:])
}
