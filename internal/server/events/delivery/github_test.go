package delivery

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/server/events/outbox"
)

func TestNotificationPolicyKeepsAutomationOutOfHumanOnlyFilter(t *testing.T) {
	base := notificationPolicy{IssueActions: []string{"opened"}, IssueKinds: []string{"ordinary"}, ActorClasses: []string{"human"}}
	envelope := outbox.Envelope{EventType: "issue.created", Notification: &outbox.NotificationFacts{
		IssueKind: "ordinary", ActorClass: "automation",
	}}
	if matched, reason := matchesNotification(envelope, base); matched || reason != "actor_class_filtered" {
		t.Fatalf("human-only automation match=%v reason=%q", matched, reason)
	}
	base.ActorClasses = []string{"automation"}
	if matched, reason := matchesNotification(envelope, base); !matched || reason != "" {
		t.Fatalf("automation filter match=%v reason=%q", matched, reason)
	}
	envelope.Notification.ActorClass = ""
	if matched, reason := matchesNotification(envelope, base); matched || reason != "actor_class_filtered" {
		t.Fatalf("unknown actor provenance match=%v reason=%q", matched, reason)
	}
}
