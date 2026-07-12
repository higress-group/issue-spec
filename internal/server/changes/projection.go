package changes

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var rawIssueMarker = regexp.MustCompile(`(?s)<!--\s*issue-spec:issue=([^\s>]+)\s+change=([^\s>]+)\s+version=([^\s>]+)\s*-->`)
var proposalLink = regexp.MustCompile(`(?im)^\s*-\s*Proposal Issue:\s*(.+?)\s*$`)
var designLink = regexp.MustCompile(`(?im)^\s*-\s*Design Issue:\s*(.+?)\s*$`)

type rawArtifact struct {
	repositoryID uuid.UUID
	issueID      uuid.UUID
	number       int64
	title        string
	body         string
	state        string
	updatedAt    time.Time
	labels       []string
	projected    bool
	kind         Stage
	changeKey    string
	version      string
}

type typedArtifact struct {
	repositoryID uuid.UUID
	issueID      uuid.UUID
	typ          string
	key          string
	status       string
	closureLink  bool
	updatedAt    time.Time
}

func parseRawArtifact(row rawArtifact) (rawArtifact, bool) {
	match := rawIssueMarker.FindStringSubmatch(row.body)
	if len(match) != 4 {
		return row, false
	}
	row.kind = Stage(strings.ToLower(strings.TrimSpace(match[1])))
	row.changeKey = NormalizeChangeKey(match[2])
	row.version = strings.TrimSpace(match[3])
	return row, row.changeKey != ""
}

func knownStage(stage Stage) bool {
	return stage == StageProposal || stage == StageDesign || stage == StageImplement
}

func validMarker(item rawArtifact) bool {
	version, err := strconv.Atoi(item.version)
	return knownStage(item.kind) && err == nil && version == 1
}

func matchingArtifactLabel(item rawArtifact) bool {
	expected := "issue-spec/" + string(item.kind)
	foundExpected := false
	for _, label := range item.labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if label == expected {
			foundExpected = true
		}
		if strings.HasPrefix(label, "issue-spec/") &&
			(label == "issue-spec/proposal" || label == "issue-spec/design" || label == "issue-spec/implement") && label != expected {
			return false
		}
	}
	return foundExpected
}

func hasRequiredLink(item rawArtifact) bool {
	var match []string
	switch item.kind {
	case StageProposal:
		return true
	case StageDesign:
		match = proposalLink.FindStringSubmatch(item.body)
	case StageImplement:
		match = designLink.FindStringSubmatch(item.body)
	default:
		return false
	}
	if len(match) != 2 {
		return false
	}
	value := strings.TrimSpace(match[1])
	return value != "" && !strings.EqualFold(value, "N/A") && !strings.EqualFold(value, "TBD")
}

func selectArtifact(items []rawArtifact) rawArtifact {
	ordered := append([]rawArtifact(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].projected != ordered[j].projected {
			return ordered[i].projected
		}
		if validMarker(ordered[i]) != validMarker(ordered[j]) {
			return validMarker(ordered[i])
		}
		if !ordered[i].updatedAt.Equal(ordered[j].updatedAt) {
			return ordered[i].updatedAt.After(ordered[j].updatedAt)
		}
		return ordered[i].number < ordered[j].number
	})
	return ordered[0]
}

func artifactSlot(item rawArtifact, orgID uuid.UUID) *Artifact {
	return &Artifact{ID: item.issueID, Number: item.number, Title: item.title, State: item.state,
		URL:           "/issues/" + orgID.String() + "/" + item.repositoryID.String() + "/" + strconv.FormatInt(item.number, 10),
		MarkerVersion: item.version, UpdatedAt: item.updatedAt, Valid: validMarker(item)}
}

func addProgress(progress *Progress, status string) {
	if status == "superseded" {
		return
	}
	progress.Total++
	switch status {
	case "done":
		progress.Completed++
	case "in-progress":
		progress.InProgress++
	case "blocked":
		progress.Blocked++
	default:
		progress.Pending++
	}
}

func dedupeSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
