package contextbundle

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/preview"
)

// FoldPreviews replaces every recognized html-preview range with an inert,
// explicit descriptor. The original provider bytes are retained only by the
// caller for provenance; they are not present in Body.
func FoldPreviews(body, sourceLocator, expansionBase string) (string, []preview.Descriptor) {
	parsed := preview.ParseWithSource(body, sourceLocator)
	descriptors := append([]preview.Descriptor(nil), parsed.Descriptors...)
	if len(descriptors) == 0 {
		return body, nil
	}

	var folded strings.Builder
	cursor := 0
	for i := range descriptors {
		descriptor := &descriptors[i]
		descriptor.Omitted = true
		if descriptor.ExpansionCommand != "" && strings.TrimSpace(expansionBase) != "" {
			descriptor.ExpansionCommand = strings.TrimSpace(expansionBase) + " --expand-preview " + descriptor.ID
		}
		if descriptor.Range.Start < cursor || descriptor.Range.End > len(body) {
			// The canonical parser owns ranges; this is defensive fail-closed
			// behavior if an invalid range ever reaches this layer.
			return preview.SemanticView(body), descriptors
		}
		folded.WriteString(body[cursor:descriptor.Range.Start])
		folded.WriteString(foldedPreviewDescriptor(*descriptor, body[descriptor.Range.Start:descriptor.Range.End]))
		cursor = descriptor.Range.End
	}
	folded.WriteString(body[cursor:])
	return folded.String(), descriptors
}

func foldedPreviewDescriptor(descriptor preview.Descriptor, originalRange string) string {
	data, _ := json.Marshal(descriptor)
	replacement := "```issue-spec-html-preview-descriptor\n" + string(data) + "\n```"
	switch {
	case strings.HasSuffix(originalRange, "\r\n"):
		replacement += "\r\n"
	case strings.HasSuffix(originalRange, "\n"):
		replacement += "\n"
	}
	return replacement
}

// IssueReadExpansionBase returns a deliberate read command for the exact issue
// body or comment represented by an artifact.
func IssueReadExpansionBase(repo string, issue int, commentID int64, sourceURL string) string {
	base := fmt.Sprintf("issue-spec read issue --repo %s --issue %s", repo, strconv.Itoa(issue))
	if parsed, err := url.Parse(strings.TrimSpace(sourceURL)); err == nil && parsed.Host != "" &&
		!strings.EqualFold(parsed.Host, "github.com") {
		base += " --hostname " + parsed.Host
	}
	if commentID > 0 {
		base += " --comment " + strconv.FormatInt(commentID, 10)
	}
	return base
}
