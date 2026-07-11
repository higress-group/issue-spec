package codec

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

// Presenter constructs all API and browser URLs from configured origins.
type Presenter struct {
	Origins publicurl.Origins
}

type UserView struct {
	StableID string
	Login    string
	Admin    bool
}

type LabelView struct {
	StableID    string
	Owner       string
	Repository  string
	Name        string
	Color       string
	Description string
	Default     bool
}

type IssueView struct {
	StableID     string
	Owner        string
	Repository   string
	Number       int64
	State        string
	StateReason  *string
	Title        string
	Body         string
	Author       UserView
	Labels       []LabelView
	Locked       bool
	CommentCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ClosedAt     *time.Time
	Reactions    Reactions
}

type CommentView struct {
	StableID    string
	Owner       string
	Repository  string
	IssueNumber int64
	Body        string
	Author      UserView
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Reactions   Reactions
}

type ReactionView struct {
	StableID  string
	Author    UserView
	Content   string
	CreatedAt time.Time
}

func (p Presenter) PresentUser(view UserView) User {
	return User{Login: view.Login, ID: StableNumericID(view.StableID), NodeID: NodeID("User", view.StableID), Type: "User", SiteAdmin: view.Admin}
}

func (p Presenter) PresentIssue(view IssueView) Issue {
	repoPath := "/repos/" + segment(view.Owner) + "/" + segment(view.Repository)
	issuePath := repoPath + "/issues/" + strconv.FormatInt(view.Number, 10)
	labels := make([]Label, len(view.Labels))
	for index, label := range view.Labels {
		labels[index] = p.PresentLabel(label)
	}
	reactions := view.Reactions
	reactions.URL = p.Origins.API.MustURL(issuePath + "/reactions")
	return Issue{
		ID: StableNumericID(view.StableID), NodeID: NodeID("Issue", view.StableID),
		URL: p.Origins.API.MustURL(issuePath), RepositoryURL: p.Origins.API.MustURL(repoPath),
		LabelsURL: p.Origins.API.MustURL(issuePath + "/labels"), CommentsURL: p.Origins.API.MustURL(issuePath + "/comments"),
		EventsURL: p.Origins.API.MustURL(issuePath + "/events"), HTMLURL: p.Origins.Web.MustURL("/" + segment(view.Owner) + "/" + segment(view.Repository) + "/issues/" + strconv.FormatInt(view.Number, 10)),
		Number: view.Number, State: view.State, StateReason: view.StateReason, Title: view.Title, Body: view.Body,
		User: p.PresentUser(view.Author), Labels: labels, Locked: view.Locked, Comments: view.CommentCount,
		CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt, ClosedAt: view.ClosedAt, Reactions: reactions,
	}
}

func (p Presenter) PresentComment(view CommentView) Comment {
	repoPath := "/repos/" + segment(view.Owner) + "/" + segment(view.Repository)
	issuePath := repoPath + "/issues/" + strconv.FormatInt(view.IssueNumber, 10)
	commentID := StableNumericID(view.StableID)
	commentPath := repoPath + "/issues/comments/" + strconv.FormatInt(commentID, 10)
	reactions := view.Reactions
	reactions.URL = p.Origins.API.MustURL(commentPath + "/reactions")
	return Comment{
		ID: commentID, NodeID: NodeID("IssueComment", view.StableID), URL: p.Origins.API.MustURL(commentPath),
		HTMLURL:  p.Origins.Web.MustURLWithFragment("/"+segment(view.Owner)+"/"+segment(view.Repository)+"/issues/"+strconv.FormatInt(view.IssueNumber, 10), "issuecomment-"+strconv.FormatInt(commentID, 10)),
		IssueURL: p.Origins.API.MustURL(issuePath), Body: view.Body, User: p.PresentUser(view.Author),
		CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt, Reactions: reactions,
	}
}

func (p Presenter) PresentLabel(view LabelView) Label {
	repoPath := "/repos/" + segment(view.Owner) + "/" + segment(view.Repository)
	return Label{
		ID: StableNumericID(view.StableID), NodeID: NodeID("Label", view.StableID),
		URL: p.Origins.API.MustURL(repoPath + "/labels/" + segment(view.Name)), Name: view.Name,
		Color: strings.TrimPrefix(view.Color, "#"), Default: view.Default, Description: view.Description,
	}
}

func (p Presenter) PresentReaction(view ReactionView) Reaction {
	return Reaction{ID: StableNumericID(view.StableID), NodeID: NodeID("Reaction", view.StableID), User: p.PresentUser(view.Author), Content: view.Content, CreatedAt: view.CreatedAt}
}

func (p Presenter) PresentPermission(permission, role string, user UserView) Permission {
	return Permission{Permission: permission, RoleName: role, User: p.PresentUser(user)}
}

// StableNumericID maps an immutable server identifier to the positive numeric
// shape consumed by the existing GitHub client. Database UUIDs remain private.
func StableNumericID(stable string) int64 {
	digest := sha256.Sum256([]byte(stable))
	value := binary.BigEndian.Uint64(digest[:8]) & (1<<63 - 1)
	if value == 0 {
		return 1
	}
	return int64(value)
}

// NodeID is an opaque stable identifier suitable for the optional GitHub field.
func NodeID(kind, stable string) string {
	return base64.RawStdEncoding.EncodeToString([]byte(kind + ":" + stable))
}

func segment(value string) string { return url.PathEscape(value) }
