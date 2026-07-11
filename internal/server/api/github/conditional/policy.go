// Package conditional defines stable conditional-response metadata shared by
// the GitHub-compatible protocol features.
package conditional

import (
	"time"

	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
)

type Policy struct {
	Clock func() time.Time
	Limit int
}

func (p Policy) Rate() pagination.Rate {
	now := time.Now().UTC()
	if p.Clock != nil {
		now = p.Clock().UTC()
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 5000
	}
	return pagination.Rate{
		Limit: limit, Remaining: limit - 1, Used: 1,
		Reset: now.Truncate(time.Hour).Add(time.Hour), Resource: "core",
	}
}
