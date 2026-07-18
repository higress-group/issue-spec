// Package buildinfo is the single runtime source for issue-spec build identity.
package buildinfo

import (
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/requirements"
)

// These values are set with -ldflags -X for release builds. Keep explicit
// development defaults so local builds never impersonate a published release.
var (
	version                    = "development"
	channel                    = "development"
	revision                   = "unknown"
	sourceDateEpoch            = "0"
	requirementsSkillContentID = ""
)

type Info struct {
	Version                    string `json:"version"`
	Channel                    string `json:"channel"`
	Revision                   string `json:"revision"`
	BuildTime                  string `json:"build_time"`
	GoVersion                  string `json:"go_version"`
	OS                         string `json:"os"`
	Arch                       string `json:"arch"`
	RequirementsSkillContentID string `json:"requirements_skill_content_id"`
}

func Current() Info {
	identity := Info{
		Version: strings.TrimSpace(version), Channel: strings.TrimSpace(channel), Revision: strings.TrimSpace(revision),
		GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		RequirementsSkillContentID: strings.TrimSpace(requirementsSkillContentID),
	}
	if identity.Version == "" {
		identity.Version = "development"
	}
	if identity.Channel == "" {
		identity.Channel = "development"
	}
	if identity.Revision == "" {
		identity.Revision = "unknown"
	}
	if epoch, err := strconv.ParseInt(strings.TrimSpace(sourceDateEpoch), 10, 64); err == nil && epoch > 0 {
		identity.BuildTime = time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	}
	if identity.RequirementsSkillContentID == "" {
		identity.RequirementsSkillContentID, _ = requirements.ContentID()
	}
	return identity
}
