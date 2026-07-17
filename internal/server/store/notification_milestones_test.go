package store

import "testing"

func TestNotificationMilestonesAreFixedAndNarrow(t *testing.T) {
	for _, milestone := range []NotificationMilestone{
		NotificationMilestoneProposal, NotificationMilestoneDesign,
		NotificationMilestoneImplement, NotificationMilestoneCompleted,
	} {
		if !milestone.Valid() {
			t.Fatalf("milestone %q is invalid", milestone)
		}
	}
	for _, milestone := range []NotificationMilestone{"", "blocked", "closed", "edited"} {
		if milestone.Valid() {
			t.Fatalf("milestone %q unexpectedly valid", milestone)
		}
	}
}
