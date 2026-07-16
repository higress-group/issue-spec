package search

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultPerPage = 20
	MaxPerPage     = 50
	MaxQueryBytes  = 256
)

var ErrInvalidOptions = errors.New("search: invalid options")

type Source string

const (
	SourceAll      Source = "all"
	SourceIssue    Source = "issue"
	SourceComment  Source = "comment"
	SourceComments Source = "comments"
	SourceChange   Source = "change"
)

type Options struct {
	Query   string `json:"query"`
	State   string `json:"state,omitempty"`
	Source  Source `json:"source,omitempty"`
	Stage   string `json:"stage,omitempty"`
	Page    int    `json:"page,omitempty"`
	PerPage int    `json:"per_page,omitempty"`
}

func (o Options) normalize() (Options, error) {
	o.Query = strings.TrimSpace(o.Query)
	if o.Query == "" || len(o.Query) > MaxQueryBytes {
		return Options{}, ErrInvalidOptions
	}
	o.State = strings.ToLower(strings.TrimSpace(o.State))
	if o.State == "" {
		o.State = "all"
	}
	if o.State != "all" && o.State != "open" && o.State != "closed" {
		return Options{}, ErrInvalidOptions
	}
	if o.Source == "" {
		o.Source = SourceAll
	}
	if o.Source == SourceComment {
		o.Source = SourceComments
	}
	if o.Source != SourceAll && o.Source != SourceIssue && o.Source != SourceComments && o.Source != SourceChange {
		return Options{}, ErrInvalidOptions
	}
	o.Stage = strings.ToLower(strings.TrimSpace(o.Stage))
	if o.Stage != "" && o.Stage != "proposal" && o.Stage != "design" && o.Stage != "implement" {
		return Options{}, ErrInvalidOptions
	}
	if o.Page == 0 {
		o.Page = 1
	}
	if o.PerPage == 0 {
		o.PerPage = DefaultPerPage
	}
	if o.Page < 1 || o.PerPage < 1 || o.PerPage > MaxPerPage {
		return Options{}, ErrInvalidOptions
	}
	return o, nil
}

// Normalize applies the public request bounds shared by HTTP and service
// callers. The service repeats this check so non-HTTP callers remain safe.
func (o Options) Normalize() (Options, error) { return o.normalize() }

type Match struct {
	Source    Source     `json:"source"`
	Excerpt   string     `json:"excerpt"`
	CommentID *uuid.UUID `json:"comment_id,omitempty"`
}

type Change struct {
	Key   string `json:"key"`
	Stage string `json:"stage"`
}

type Issue struct {
	OrganizationID uuid.UUID `json:"organization_id"`
	Organization   string    `json:"organization"`
	RepositoryID   uuid.UUID `json:"repository_id"`
	Repository     string    `json:"repository"`
	ID             uuid.UUID `json:"id"`
	Number         int64     `json:"number"`
	Title          string    `json:"title"`
	State          string    `json:"state"`
	UpdatedAt      time.Time `json:"updated_at"`
	URL            string    `json:"url"`
	Changes        []Change  `json:"changes"`
	Score          int       `json:"score"`
	Matches        []Match   `json:"matches"`
}

type Page struct {
	Items   []Issue `json:"items"`
	Page    int     `json:"page"`
	PerPage int     `json:"per_page"`
	HasNext bool    `json:"has_next"`
}
