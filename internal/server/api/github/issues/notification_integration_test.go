package issues

import "testing"

func TestNotificationIntegrationRequiresOneCompleteOptionalConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		input []NotificationIntegration
		valid bool
	}{
		{name: "omitted", valid: true},
		{name: "disabled", input: []NotificationIntegration{{}}, valid: true},
		{name: "enabled without projectors", input: []NotificationIntegration{{Enabled: true}}},
		{name: "multiple", input: []NotificationIntegration{{}, {}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateNotificationIntegrations(test.input)
			if (err == nil) != test.valid {
				t.Fatalf("validateNotificationIntegrations() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}
