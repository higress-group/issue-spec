package notificationmail

import (
	"context"
	"testing"

	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
)

func TestDispatcherRequiresAndRoutesEveryFixedKind(t *testing.T) {
	preparers := map[emaildelivery.Kind]emaildelivery.Preparer{}
	for _, kind := range []emaildelivery.Kind{emaildelivery.KindVerification, emaildelivery.KindMention,
		emaildelivery.KindRepoIssueCreated, emaildelivery.KindChangeMilestone} {
		preparers[kind] = taggedPreparer{kind: kind}
	}
	dispatcher, err := NewDispatcher(preparers)
	if err != nil {
		t.Fatal(err)
	}
	for kind := range preparers {
		message, err := dispatcher.Prepare(t.Context(), emaildelivery.Delivery{Kind: kind})
		if err != nil || message.Subject != string(kind) {
			t.Fatalf("kind %q = %+v, %v", kind, message, err)
		}
	}
	delete(preparers, emaildelivery.KindMention)
	if _, err := NewDispatcher(preparers); err == nil {
		t.Fatal("partial dispatcher accepted")
	}
}

type taggedPreparer struct{ kind emaildelivery.Kind }

func (p taggedPreparer) Prepare(context.Context, emaildelivery.Delivery) (emaildelivery.Message, error) {
	return emaildelivery.Message{Subject: string(p.kind)}, nil
}
