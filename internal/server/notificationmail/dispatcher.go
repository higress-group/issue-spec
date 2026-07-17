package notificationmail

import (
	"context"
	"errors"

	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
)

// Dispatcher is the complete four-kind composition boundary. Requiring every
// fixed kind before construction prevents an enabled worker from consuming a
// queue row with a partial feature registry.
type Dispatcher struct {
	preparers map[emaildelivery.Kind]emaildelivery.Preparer
}

func NewDispatcher(preparers map[emaildelivery.Kind]emaildelivery.Preparer) (*Dispatcher, error) {
	required := []emaildelivery.Kind{emaildelivery.KindVerification, emaildelivery.KindMention,
		emaildelivery.KindRepoIssueCreated, emaildelivery.KindChangeMilestone}
	if len(preparers) != len(required) {
		return nil, errors.New("notification mail: all four preparers are required")
	}
	copyOf := make(map[emaildelivery.Kind]emaildelivery.Preparer, len(required))
	for _, kind := range required {
		preparer := preparers[kind]
		if preparer == nil {
			return nil, errors.New("notification mail: all four preparers are required")
		}
		copyOf[kind] = preparer
	}
	return &Dispatcher{preparers: copyOf}, nil
}

func (d *Dispatcher) Prepare(ctx context.Context, delivery emaildelivery.Delivery) (emaildelivery.Message, error) {
	if d == nil || !delivery.Kind.Valid() {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	preparer := d.preparers[delivery.Kind]
	if preparer == nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	return preparer.Prepare(ctx, delivery)
}
