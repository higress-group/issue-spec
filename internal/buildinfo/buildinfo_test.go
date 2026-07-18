package buildinfo

import (
	"strings"
	"testing"
)

func TestDevelopmentIdentityIsExplicit(t *testing.T) {
	got := Current()
	if got.Version != "development" || got.Channel != "development" || got.Revision != "unknown" || got.BuildTime != "" {
		t.Fatalf("development identity = %+v", got)
	}
	if !strings.HasPrefix(got.RequirementsSkillContentID, "sha256:") {
		t.Fatalf("requirements skill content ID = %q", got.RequirementsSkillContentID)
	}
	if got.GoVersion == "" || got.OS == "" || got.Arch == "" {
		t.Fatalf("runtime identity incomplete: %+v", got)
	}
}

func TestInjectedIdentity(t *testing.T) {
	oldVersion, oldChannel, oldRevision := version, channel, revision
	oldEpoch, oldSkill := sourceDateEpoch, requirementsSkillContentID
	t.Cleanup(func() {
		version, channel, revision = oldVersion, oldChannel, oldRevision
		sourceDateEpoch, requirementsSkillContentID = oldEpoch, oldSkill
	})
	version = "v1.2.3"
	channel = "stable"
	revision = strings.Repeat("a", 40)
	sourceDateEpoch = "1710000000"
	requirementsSkillContentID = "sha256:release"
	got := Current()
	if got.Version != version || got.Channel != channel || got.Revision != revision || got.BuildTime != "2024-03-09T16:00:00Z" || got.RequirementsSkillContentID != requirementsSkillContentID {
		t.Fatalf("injected identity = %+v", got)
	}
}
