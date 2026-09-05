package templates

import _ "embed"

//go:embed assets/implementation-review.md
var implementationReviewReference string

//go:embed assets/human-review-projections.md
var humanReviewProjectionsReference string

//go:embed assets/projection-proposal.md
var proposalProjectionReference string

//go:embed assets/projection-design.md
var designProjectionReference string

//go:embed assets/projection-implement.md
var implementProjectionReference string

//go:embed assets/projection-example.md
var projectionExample string

// HumanReviewProjectionResources is the exact generated resource inventory,
// shared by generation and opt-out cleanup; unrelated user files are not owned.
func HumanReviewProjectionResources() []RenderedSkillResource {
	return []RenderedSkillResource{
		{Path: "references/human-review-projections.md", Content: humanReviewProjectionsReference},
		{Path: "references/projections/proposal.md", Content: proposalProjectionReference},
		{Path: "references/projections/design.md", Content: designProjectionReference},
		{Path: "references/projections/implement.md", Content: implementProjectionReference},
		{Path: "assets/projection-example.md", Content: projectionExample},
	}
}
