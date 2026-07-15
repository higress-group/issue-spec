// Package codec defines the GitHub-compatible JSON boundary. Database models
// are intentionally not serialized directly.
package codec

import "time"

type User struct {
	Login     string `json:"login"`
	Name      string `json:"name,omitempty"`
	ID        int64  `json:"id"`
	NodeID    string `json:"node_id,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	HTMLURL   string `json:"html_url,omitempty"`
	Type      string `json:"type"`
	SiteAdmin bool   `json:"site_admin"`
}

type Reactions struct {
	URL        string `json:"url"`
	TotalCount int    `json:"total_count"`
	PlusOne    int    `json:"+1"`
	MinusOne   int    `json:"-1"`
	Laugh      int    `json:"laugh"`
	Hooray     int    `json:"hooray"`
	Confused   int    `json:"confused"`
	Heart      int    `json:"heart"`
	Rocket     int    `json:"rocket"`
	Eyes       int    `json:"eyes"`
}

type Label struct {
	ID          int64  `json:"id"`
	NodeID      string `json:"node_id,omitempty"`
	URL         string `json:"url"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Default     bool   `json:"default"`
	Description string `json:"description"`
}

type Issue struct {
	ID            int64      `json:"id"`
	NodeID        string     `json:"node_id,omitempty"`
	URL           string     `json:"url"`
	RepositoryURL string     `json:"repository_url"`
	LabelsURL     string     `json:"labels_url"`
	CommentsURL   string     `json:"comments_url"`
	EventsURL     string     `json:"events_url"`
	HTMLURL       string     `json:"html_url"`
	Number        int64      `json:"number"`
	State         string     `json:"state"`
	StateReason   *string    `json:"state_reason"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	User          User       `json:"user"`
	Labels        []Label    `json:"labels"`
	Locked        bool       `json:"locked"`
	Comments      int        `json:"comments"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ClosedAt      *time.Time `json:"closed_at"`
	Reactions     Reactions  `json:"reactions"`
}

type Comment struct {
	ID        int64     `json:"id"`
	NodeID    string    `json:"node_id,omitempty"`
	URL       string    `json:"url"`
	HTMLURL   string    `json:"html_url"`
	IssueURL  string    `json:"issue_url"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Reactions Reactions `json:"reactions"`
}

type Reaction struct {
	ID        int64     `json:"id"`
	NodeID    string    `json:"node_id,omitempty"`
	User      User      `json:"user"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type Permission struct {
	Permission string `json:"permission"`
	RoleName   string `json:"role_name"`
	User       User   `json:"user"`
}

type Subscription struct {
	Subscribed    bool      `json:"subscribed"`
	Ignored       bool      `json:"ignored"`
	Reason        string    `json:"reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	URL           string    `json:"url"`
	RepositoryURL string    `json:"repository_url"`
}

// Capabilities is returned by native server metadata and consumed by profile
// aware clients before attempting code-host operations.
type Capabilities struct {
	Issues         bool `json:"issues"`
	Comments       bool `json:"comments"`
	Labels         bool `json:"labels"`
	Reactions      bool `json:"reactions"`
	Permissions    bool `json:"permissions"`
	Subscriptions  bool `json:"subscriptions"`
	RunnerServe    bool `json:"runner_serve"`
	Notifications  bool `json:"notifications"`
	PullRequests   bool `json:"pull_requests"`
	Reviews        bool `json:"reviews"`
	CommitStatuses bool `json:"commit_statuses"`
	CheckRuns      bool `json:"check_runs"`
}

// SelfHostedCapabilities declares the issue-only boundary explicitly.
func SelfHostedCapabilities() Capabilities {
	return Capabilities{
		Issues: true, Comments: true, Labels: true, Reactions: true,
		Permissions: true, Subscriptions: true, RunnerServe: true,
		Notifications: false, PullRequests: false, Reviews: false,
		CommitStatuses: false, CheckRuns: false,
	}
}
